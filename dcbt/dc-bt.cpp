// dc-bt - the libtorrent 2.0 torrent engine of Download Center.
//
// One session, controlled by dcd over a unix socket with line-delimited
// JSON (see PROTOCOL.md). dcd owns queueing and scheduling: every torrent is
// added without auto-management and paused/resumed on request. dc-bt owns
// what libtorrent does per torrent: resume data, seeding targets
// (ratio/time) and writing the .torrent of magnets.

#include <libtorrent/session.hpp>
#include <libtorrent/session_params.hpp>
#include <libtorrent/settings_pack.hpp>
#include <libtorrent/add_torrent_params.hpp>
#include <libtorrent/torrent_handle.hpp>
#include <libtorrent/torrent_status.hpp>
#include <libtorrent/torrent_info.hpp>
#include <libtorrent/alert_types.hpp>
#include <libtorrent/magnet_uri.hpp>
#include <libtorrent/read_resume_data.hpp>
#include <libtorrent/write_resume_data.hpp>
#include <libtorrent/peer_info.hpp>
#include <libtorrent/announce_entry.hpp>
#include <libtorrent/hex.hpp>
#include <libtorrent/version.hpp>
#include <libtorrent/bdecode.hpp>
#include <libtorrent/download_priority.hpp>
#include <libtorrent/extensions/ut_metadata.hpp>
#include <libtorrent/extensions/ut_pex.hpp>
#include <libtorrent/extensions/smart_ban.hpp>
#include <libtorrent/ip_filter.hpp>
#include <libtorrent/parse_url.hpp>
#include <libtorrent/session_stats.hpp>
#include <libtorrent/extensions.hpp>

#include "json.hpp"

#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <ifaddrs.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <netdb.h>
#include <poll.h>
#include <unistd.h>
#include <fcntl.h>
#include <dirent.h>
#include <signal.h>
#include <cerrno>
#include <cstdarg>
#include <cstdio>
#include <cstring>
#include <algorithm>
#include <chrono>
#include <fstream>
#include <map>
#include <memory>
#include <mutex>
#include <set>
#include <sstream>
#include <thread>
#include <string>
#include <vector>

namespace lt = libtorrent;
using json = nlohmann::json;
using clk = std::chrono::steady_clock;

static const char *DCBT_VERSION = "1.4";

static volatile sig_atomic_t g_stop = 0;
static void on_signal(int) { g_stop = 1; }

static void dlog(const char *fmt, ...) __attribute__((format(printf, 1, 2)));
static void dlog(const char *fmt, ...)
{
	char ts[32];
	time_t now = time(nullptr);
	struct tm tm;
	localtime_r(&now, &tm);
	strftime(ts, sizeof ts, "%Y/%m/%d %H:%M:%S", &tm);
	fprintf(stderr, "%s dc-bt: ", ts);
	va_list ap;
	va_start(ap, fmt);
	vfprintf(stderr, fmt, ap);
	va_end(ap);
	fputc('\n', stderr);
	fflush(stderr);
}

// --- small helpers ---

static bool read_file(std::string const &path, std::vector<char> &out)
{
	std::ifstream f(path, std::ios::binary);
	if (!f)
		return false;
	out.assign(std::istreambuf_iterator<char>(f), std::istreambuf_iterator<char>());
	return true;
}

static bool write_file(std::string const &path, char const *data, size_t len)
{
	std::string tmp = path + ".tmp";
	int fd = ::open(tmp.c_str(), O_WRONLY | O_CREAT | O_TRUNC, 0600);
	if (fd < 0)
		return false;
	size_t off = 0;
	while (off < len)
	{
		ssize_t n = ::write(fd, data + off, len - off);
		if (n <= 0)
		{
			::close(fd);
			::unlink(tmp.c_str());
			return false;
		}
		off += size_t(n);
	}
	::fsync(fd);
	::close(fd);
	return ::rename(tmp.c_str(), path.c_str()) == 0;
}

static const char b64tab[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

static std::vector<char> b64decode(std::string const &in)
{
	std::vector<char> out;
	int val = 0, bits = -8;
	for (unsigned char c : in)
	{
		const char *p = strchr(b64tab, c);
		if (c == '=' || !p || !c)
			continue;
		val = (val << 6) + int(p - b64tab);
		bits += 6;
		if (bits >= 0)
		{
			out.push_back(char((val >> bits) & 0xff));
			bits -= 8;
		}
	}
	return out;
}

static std::string key_of(lt::info_hash_t const &ih)
{
	static const char hx[] = "0123456789abcdef";
	char const *p = ih.has_v1() ? ih.v1.data() : ih.v2.data();
	std::string out;
	for (int i = 0; i < 20; ++i)
	{
		unsigned char c = (unsigned char)p[i];
		out.push_back(hx[c >> 4]);
		out.push_back(hx[c & 15]);
	}
	return out;
}

static std::string bits_hex(lt::typed_bitfield<lt::piece_index_t> const &bf)
{
	static const char hx[] = "0123456789abcdef";
	int n = bf.size();
	std::string out;
	out.reserve(size_t((n + 3) / 4));
	for (int i = 0; i < n; i += 4)
	{
		int v = 0;
		for (int b = 0; b < 4; ++b)
		{
			v <<= 1;
			if (i + b < n && bf.get_bit(lt::piece_index_t(i + b)))
				v |= 1;
		}
		out.push_back(hx[v]);
	}
	return out;
}

static const char *state_name(lt::torrent_status::state_t s)
{
	switch (s)
	{
	case lt::torrent_status::checking_files: return "checking_files";
	case lt::torrent_status::downloading_metadata: return "downloading_metadata";
	case lt::torrent_status::downloading: return "downloading";
	case lt::torrent_status::finished: return "finished";
	case lt::torrent_status::seeding: return "seeding";
	case lt::torrent_status::checking_resume_data: return "checking_resume_data";
	default: return "unknown";
	}
}

// --- per-torrent options kept beside the resume data ---

struct Options
{
	double seed_ratio = 0;
	int seed_time = 0; // minutes; -1 no seeding, 0 no time limit
	bool complete = false;
	bool metadata_only = false;
	bool guard = false;       // a regular user's torrent: see the address guard
	bool guard_known = true;  // the saved options said (before 1.4 they did not; not saved)
	bool has_select = false;
	std::vector<int> select;
	std::map<int, int> priorities;
	std::string magnet_save; // metadata_only: where to write the .torrent
	std::string move_error;  // why the last move failed (not saved)
};

// --- address guard ---
//
// Regular users' torrents must not reach the NAS itself: the same rule as
// dc-dl's guard and netutil.Forbidden in dcd (loopback, link-local,
// unspecified, multicast and the NAS's own addresses; LAN hosts are fine).
// The session IP filter blocks these addresses and only torrents added with
// guard apply it (torrent_flags::apply_ip_filter), so trackers, web seeds and
// peers are checked after their names resolve. Through a proxy that resolves
// names itself the target stays unknown, so literal addresses (and localhost)
// are also removed from guarded torrents when they are added. An HTTP proxy
// for trackers that runs on the NAS itself is let through the filter, or no
// torrent could reach it.

static bool forbidden_v4(lt::address_v4 const &a)
{
	auto b = a.to_bytes();
	return b[0] == 127 || b[0] == 0 || (b[0] == 169 && b[1] == 254) || b[0] >= 224;
}

static bool forbidden_addr(lt::address const &a, std::vector<lt::address> const &own)
{
	if (a.is_v4())
	{
		if (forbidden_v4(a.to_v4()))
			return true;
	}
	else
	{
		lt::address_v6 v6 = a.to_v6();
		if (v6.is_loopback() || v6.is_unspecified() || v6.is_link_local() || v6.is_multicast())
			return true;
		if (v6.is_v4_mapped())
			return forbidden_addr(lt::make_address_v4(lt::v4_mapped, v6), own);
	}
	return std::find(own.begin(), own.end(), a) != own.end();
}

// literal_address reads a host that names an address by itself: IPv6, or
// IPv4 in any form the C library's resolver takes as one (2130706433,
// 0x7f.1, 127.1 are all 127.0.0.1), so a name lookup cannot see it otherwise.
static bool literal_address(std::string const &host, lt::address &out)
{
	lt::error_code ec;
	out = lt::make_address(host, ec);
	if (!ec)
		return true;
	struct in_addr in;
	if (host.empty() || inet_aton(host.c_str(), &in) == 0)
		return false;
	lt::address_v4::bytes_type b;
	std::memcpy(b.data(), &in, 4);
	out = lt::address_v4(b);
	return true;
}

// forbidden_url: a tracker or web seed whose host is a forbidden literal
// address or localhost.
static bool forbidden_url(std::string const &url, std::vector<lt::address> const &own)
{
	lt::error_code ec;
	std::string host;
	std::tie(std::ignore, std::ignore, host, std::ignore, std::ignore) = lt::parse_url_components(url, ec);
	if (ec)
		return false;
	std::string lower = host;
	std::transform(lower.begin(), lower.end(), lower.begin(), [](unsigned char c) { return char(std::tolower(c)); });
	while (!lower.empty() && lower.back() == '.')
		lower.pop_back();
	if (lower == "localhost" || (lower.size() > 10 && lower.compare(lower.size() - 10, 10, ".localhost") == 0))
		return true;
	lt::address a;
	return literal_address(host, a) && forbidden_addr(a, own);
}

static std::vector<lt::address> own_addresses()
{
	std::vector<lt::address> out;
	struct ifaddrs *ifa = nullptr;
	if (getifaddrs(&ifa) != 0)
		return out;
	for (struct ifaddrs *i = ifa; i; i = i->ifa_next)
	{
		if (!i->ifa_addr)
			continue;
		if (i->ifa_addr->sa_family == AF_INET)
		{
			auto const *s = reinterpret_cast<struct sockaddr_in const *>(i->ifa_addr);
			lt::address_v4::bytes_type b;
			std::memcpy(b.data(), &s->sin_addr, 4);
			out.push_back(lt::address_v4(b));
		}
		else if (i->ifa_addr->sa_family == AF_INET6)
		{
			auto const *s = reinterpret_cast<struct sockaddr_in6 const *>(i->ifa_addr);
			lt::address_v6::bytes_type b;
			std::memcpy(b.data(), &s->sin6_addr, 16);
			out.push_back(lt::address_v6(b));
		}
	}
	freeifaddrs(ifa);
	std::sort(out.begin(), out.end());
	out.erase(std::unique(out.begin(), out.end()), out.end());
	return out;
}

// guard_filter blocks the forbidden ranges and the NAS's own addresses (also
// as IPv4-mapped IPv6), except allow: the HTTP proxy trackers go through
// when it runs on the NAS itself, which libtorrent checks against the filter.
static lt::ip_filter guard_filter(std::vector<lt::address> const &own, std::vector<lt::address> const &allow)
{
	lt::ip_filter f;
	auto block = [&](char const *a, char const *b) { f.add_rule(lt::make_address(a), lt::make_address(b), lt::ip_filter::blocked); };
	block("0.0.0.0", "0.255.255.255");
	block("127.0.0.0", "127.255.255.255");
	block("169.254.0.0", "169.254.255.255");
	block("224.0.0.0", "255.255.255.255");
	block("::", "::1");
	block("fe80::", "febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff");
	block("ff00::", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff");
	block("::ffff:0.0.0.0", "::ffff:0.255.255.255");
	block("::ffff:127.0.0.0", "::ffff:127.255.255.255");
	block("::ffff:169.254.0.0", "::ffff:169.254.255.255");
	block("::ffff:224.0.0.0", "::ffff:255.255.255.255");
	auto rule = [&](lt::address const &a, std::uint32_t flags) {
		f.add_rule(a, a, flags);
		if (a.is_v4())
		{
			lt::address m = lt::make_address_v6(lt::v4_mapped, a.to_v4());
			f.add_rule(m, m, flags);
		}
	};
	for (auto const &a : own)
		rule(a, lt::ip_filter::blocked);
	for (auto const &a : allow)
		rule(a, 0);
	return f;
}

// New paths for every file when the top folder (or the single file) of a
// torrent is called root instead of the torrent's name.
static std::map<lt::file_index_t, std::string> root_renames(lt::file_storage const &fs, std::string const &root)
{
	std::map<lt::file_index_t, std::string> out;
	std::string const &name = fs.name();
	for (lt::file_index_t i : fs.file_range())
	{
		std::string p = fs.file_path(i);
		if (p.compare(0, name.size(), name) != 0 || (p.size() > name.size() && p[name.size()] != '/'))
			continue;
		out[i] = root + p.substr(name.size());
	}
	return out;
}

static json opt_json(Options const &o)
{
	json j;
	j["seed_ratio"] = o.seed_ratio;
	j["seed_time"] = o.seed_time;
	j["complete"] = o.complete;
	j["guard"] = o.guard;
	if (o.has_select)
		j["select"] = o.select;
	json pr = json::object();
	for (auto &kv : o.priorities)
		pr[std::to_string(kv.first)] = kv.second;
	j["priorities"] = pr;
	return j;
}

static Options opt_from(json const &j)
{
	Options o;
	o.seed_ratio = j.value("seed_ratio", 0.0);
	o.seed_time = j.value("seed_time", 0);
	o.complete = j.value("complete", false);
	o.guard = j.value("guard", false);
	o.guard_known = j.contains("guard");
	if (j.contains("select") && j["select"].is_array())
	{
		o.has_select = true;
		for (auto &v : j["select"])
			if (v.is_number_integer())
				o.select.push_back(v.get<int>());
	}
	if (j.contains("priorities") && j["priorities"].is_object())
		for (auto it = j["priorities"].begin(); it != j["priorities"].end(); ++it)
			if (it.value().is_number_integer())
				o.priorities[atoi(it.key().c_str())] = it.value().get<int>();
	return o;
}

// --- hardware ---

// Hardware is what the session is tuned for, read once at start. QNAP's
// packages are per architecture, but cores and memory vary a lot within one
// (a 2 GB ARM box and a 64 GB x86 one both run the same binary family).
struct Hardware
{
	int cores = 1;
	long long ram_mb = 0; // 0 = unknown
	// libtorrent 2.0 picks its disk back end by pointer size: memory-mapped
	// files with aio and hashing threads on 64-bit, a single-threaded posix
	// back end on 32-bit (the ARMv7 models), where every write, read and
	// piece hash runs on the network thread and none of the disk settings
	// below apply.
	bool mmap = sizeof(void *) == 8;
};

static Hardware detect_hardware()
{
	Hardware h;
	long n = sysconf(_SC_NPROCESSORS_ONLN);
	if (n > 0)
		h.cores = int(n);
	std::ifstream f("/proc/meminfo");
	std::string line;
	while (std::getline(f, line))
	{
		long long kb = 0;
		if (sscanf(line.c_str(), "MemTotal: %lld kB", &kb) == 1)
		{
			h.ram_mb = kb / 1024;
			break;
		}
	}
	return h;
}

// Tuning is the part of the session settings that follows the hardware;
// the rest stays at libtorrent's defaults or comes from dcd.
struct Tuning
{
	int hashing_threads = 1;                    // libtorrent's default
	int max_queued_disk_bytes = 1024 * 1024;    // libtorrent's default
	int checking_mem_usage = 256;               // 16 KiB blocks; libtorrent's default
};

static Tuning tuning_for(Hardware const &h)
{
	Tuning t;
	if (!h.mmap)
		return t;
	// SHA-1 of finished pieces (and of everything during a check): one
	// thread falls behind a gigabit line on small ARM cores, more threads
	// than half the cores take CPU from the network thread
	t.hashing_threads = std::max(1, std::min(4, h.cores / 2));
	// Bytes waiting to be written before peers stop being read; a larger
	// queue rides out the moments the disks are busy flushing. libtorrent's
	// high_performance_seed preset uses 7 MiB.
	long long m = h.ram_mb;
	t.max_queued_disk_bytes = (m >= 8192 ? 16 : m >= 4096 ? 8 : m >= 2048 ? 4 : m >= 1024 ? 2 : 1) * 1024 * 1024;
	// Blocks read ahead while checking (high_performance_seed: 2048)
	t.checking_mem_usage = m >= 8192 ? 2048 : m >= 4096 ? 1024 : m >= 2048 ? 512 : 256;
	return t;
}

static json hardware_json(Hardware const &h, Tuning const &t)
{
	return json{{"cores", h.cores}, {"ram_mb", h.ram_mb}, {"disk_io", h.mmap ? "mmap" : "posix"},
		{"hashing_threads", t.hashing_threads}, {"max_queued_disk_bytes", t.max_queued_disk_bytes},
		{"checking_mem_usage", t.checking_mem_usage}};
}

// --- the engine ---

// --- peers found, without the NAS itself ---
//
// libtorrent adds whatever DHT and trackers return to a torrent's peer list,
// the NAS's own endpoints included: DHT hands our own announce back to us, and
// trackers return the addresses we announced from (IPv4, IPv6, each
// interface) to one another, and some DHT nodes return our announce under
// another address, with our port. For the source health dc-bt counts the
// distinct endpoints found without those, so a torrent nobody else shares
// reads as such. Self is an address of the interfaces or one of the external
// addresses libtorrent learnt, or our listen port at any address: another
// client behind the same NAT, or one listening on the same port (Download
// Center's default), is not counted either; only the count, not whether
// libtorrent connects to it. The plugin runs in libtorrent's network thread: it never calls
// a torrent_handle (those calls wait for that thread), it reaches its counts
// through the torrent's userdata.

struct Found
{
	std::mutex mu;
	std::set<lt::tcp::endpoint> all, dht; // distinct endpoints, any source / from DHT
	long long dht_at = 0;                 // the last DHT peer that is not us (Unix time)
};
static size_t const found_cap = 1000; // past this, nobody needs an exact count

static std::mutex self_mu;
static std::vector<lt::address> self_addrs; // interfaces and external addresses
static std::set<int> self_ports;            // our listen ports

static lt::address plain(lt::address const &a)
{
	if (a.is_v6() && a.to_v6().is_v4_mapped())
		return lt::make_address_v4(lt::v4_mapped, a.to_v6());
	return a;
}

static bool is_self(lt::tcp::endpoint const &ep)
{
	lt::address p = plain(ep.address());
	std::lock_guard<std::mutex> l(self_mu);
	if (self_ports.count(ep.port()))
		return true;
	for (auto const &x : self_addrs)
		if (x == p)
			return true;
	return false;
}

// The counts of every torrent, by the pointer its userdata carries; a
// plugin keeps its own reference, so forgetting a torrent never pulls the
// counts from under it.
static std::mutex found_mu;
static std::map<Found *, std::shared_ptr<Found>> found_reg;

struct FoundPlugin final : lt::torrent_plugin
{
	std::shared_ptr<Found> f;
	explicit FoundPlugin(std::shared_ptr<Found> p) : f(std::move(p)) {}
	void on_add_peer(lt::tcp::endpoint const &ep, lt::peer_source_flags_t src, lt::add_peer_flags_t flags) override
	{
		// Refused by the IP filter, or remembered from before: not found now
		if ((flags & lt::torrent_plugin::filtered) || src == lt::peer_info::resume_data || is_self(ep))
			return;
		lt::tcp::endpoint e(plain(ep.address()), ep.port());
		std::lock_guard<std::mutex> l(f->mu);
		if (f->all.size() < found_cap)
			f->all.insert(e);
		if (src & lt::peer_info::dht)
		{
			if (f->dht.size() < found_cap)
				f->dht.insert(e);
			f->dht_at = (long long)std::time(nullptr);
		}
	}
};

struct FoundSession final : lt::plugin
{
	std::shared_ptr<lt::torrent_plugin> new_torrent(lt::torrent_handle const &, lt::client_data_t ud) override
	{
		Found *p = ud.get<Found>();
		if (!p)
			return nullptr;
		std::lock_guard<std::mutex> l(found_mu);
		auto it = found_reg.find(p);
		if (it == found_reg.end())
			return nullptr;
		return std::make_shared<FoundPlugin>(it->second);
	}
};

struct Engine
{
	std::unique_ptr<lt::session> ses;
	std::string state_dir, torrents_dir, socket_path;
	std::map<std::string, lt::torrent_handle> handles;
	std::map<std::string, Options> opts;
	int listen_fd = -1, client_fd = -1;
	std::string inbuf;
	int pending_saves = 0;
	json settings = json::object();
	Hardware hw = detect_hardware();
	Tuning tune = tuning_for(hw);
	std::vector<lt::address> own;  // the NAS's addresses, for the guard
	std::vector<lt::address> exempt; // the tracker proxy's addresses (see guard_filter)
	bool filter_set = false;       // the guard filter is in the session
	clk::time_point dht_next;      // next check for DHT nodes
	int dht_tries = 0;             // bootstrap retries in a row
	bool dht_asked = false;        // waiting for session_stats_alert
	bool dht_up = false;           // DHT had nodes at the last check
	bool dht_on = false;           // the settings turn DHT on (pack_from)

	std::map<std::string, Found *> found;  // see FoundPlugin; userdata of each torrent
	std::vector<lt::address> external;     // our external addresses, as libtorrent learnt them

	// The tracker proxy's addresses, looked up away from the loop: a name
	// may take long to resolve. Shared with the lookup thread.
	struct ProxyAddrs
	{
		std::mutex mu;
		std::string host;
		std::vector<lt::address> addrs;
		bool busy = false, fresh = false;
		clk::time_point at;
	};
	std::shared_ptr<ProxyAddrs> pxa = std::make_shared<ProxyAddrs>();

	// exempt_host is the HTTP proxy trackers go through: libtorrent checks
	// its address against the filter. Other proxies are not checked (SOCKS5
	// connections are checked by their target, or the proxy resolves it).
	std::string exempt_host() const
	{
		json px = settings.value("proxy", json::object());
		if (px.value("type", std::string("none")) != "http" || !px.value("trackers", true))
			return "";
		return px.value("host", std::string());
	}

	void resolve_proxy(bool force)
	{
		std::string host = exempt_host();
		std::shared_ptr<ProxyAddrs> st = pxa;
		lt::address a;
		std::lock_guard<std::mutex> l(st->mu);
		if (host.empty() || literal_address(host, a))
		{
			std::vector<lt::address> v;
			if (!host.empty())
				v.push_back(a);
			st->fresh = st->fresh || v != st->addrs;
			st->host = host;
			st->addrs = v;
			return;
		}
		if (st->busy || (!force && st->host == host && clk::now() - st->at < std::chrono::minutes(10)))
			return;
		st->busy = true;
		st->at = clk::now();
		std::thread([st, host]() {
			std::vector<lt::address> v;
			struct addrinfo hints, *res = nullptr;
			std::memset(&hints, 0, sizeof hints);
			hints.ai_socktype = SOCK_STREAM;
			if (getaddrinfo(host.c_str(), nullptr, &hints, &res) == 0)
			{
				for (struct addrinfo *i = res; i; i = i->ai_next)
				{
					lt::error_code ec;
					char buf[INET6_ADDRSTRLEN];
					if (i->ai_family == AF_INET)
						inet_ntop(AF_INET, &reinterpret_cast<struct sockaddr_in *>(i->ai_addr)->sin_addr, buf, sizeof buf);
					else if (i->ai_family == AF_INET6)
						inet_ntop(AF_INET6, &reinterpret_cast<struct sockaddr_in6 *>(i->ai_addr)->sin6_addr, buf, sizeof buf);
					else
						continue;
					lt::address x = lt::make_address(buf, ec);
					if (!ec)
						v.push_back(x);
				}
				freeaddrinfo(res);
			}
			std::lock_guard<std::mutex> l(st->mu);
			st->fresh = st->fresh || v != st->addrs || st->host != host;
			st->host = host;
			st->addrs = v;
			st->busy = false;
		}).detach();
	}

	// update_guard puts the guard filter into the session when the NAS's
	// addresses or the tracker proxy's changed.
	// found_for gives the counts a torrent's userdata points at, made once.
	Found *found_for(std::string const &k)
	{
		auto it = found.find(k);
		if (it != found.end())
			return it->second;
		auto f = std::make_shared<Found>();
		{
			std::lock_guard<std::mutex> l(found_mu);
			found_reg[f.get()] = f;
		}
		found[k] = f.get();
		return f.get();
	}

	void forget_found(std::string const &k)
	{
		auto fi = found.find(k);
		if (fi == found.end())
			return;
		std::lock_guard<std::mutex> l(found_mu);
		found_reg.erase(fi->second);
		found.erase(fi);
	}

	// set_self tells the plugin which addresses are ours, and takes them out
	// of the counts: trackers may hand our external address back before
	// libtorrent learnt it.
	void set_self()
	{
		std::vector<lt::address> all;
		for (auto const &a : own)
			all.push_back(plain(a));
		for (auto const &a : external)
			all.push_back(plain(a));
		{
			std::lock_guard<std::mutex> l(self_mu);
			self_addrs = all;
		}
		for (auto &kv : found)
		{
			std::lock_guard<std::mutex> l(kv.second->mu);
			for (auto *set : {&kv.second->all, &kv.second->dht})
				for (auto it = set->begin(); it != set->end();)
				{
					if (is_self(*it))
						it = set->erase(it);
					else
						++it;
				}
		}
	}

	void update_guard(bool force)
	{
		resolve_proxy(force);
		std::vector<lt::address> now = own_addresses(), px;
		{
			std::lock_guard<std::mutex> l(pxa->mu);
			px = pxa->addrs;
			pxa->fresh = false;
		}
		if (!force && filter_set && now == own && px == exempt)
			return;
		own = now;
		exempt = px;
		set_self();
		filter_set = true;
		ses->set_ip_filter(guard_filter(own, exempt));
	}

	// proxy_resolved: a lookup of the tracker proxy ended since update_guard.
	bool proxy_resolved()
	{
		std::lock_guard<std::mutex> l(pxa->mu);
		return pxa->fresh;
	}

	// strip_forbidden removes the trackers and web seeds of a guarded
	// torrent whose host is a forbidden literal address or localhost.
	void strip_forbidden(lt::torrent_handle const &h)
	{
		std::vector<lt::announce_entry> tr = h.trackers(), keep;
		for (auto &a : tr)
			if (!forbidden_url(a.url, own))
				keep.push_back(a);
		if (keep.size() != tr.size())
			h.replace_trackers(keep);
		for (auto &u : h.url_seeds())
			if (forbidden_url(u, own))
				h.remove_url_seed(u);
		for (auto &u : h.http_seeds())
			if (forbidden_url(u, own))
				h.remove_http_seed(u);
	}

	void set_guard(lt::torrent_handle const &h, bool on)
	{
		if (on)
		{
			h.set_flags(lt::torrent_flags::apply_ip_filter);
			strip_forbidden(h);
		}
		else
			h.unset_flags(lt::torrent_flags::apply_ip_filter);
	}

	std::string resume_path(std::string const &k) { return state_dir + "/" + k + ".resume"; }
	std::string opts_path(std::string const &k) { return state_dir + "/" + k + ".json"; }

	void save_opts(std::string const &k)
	{
		auto it = opts.find(k);
		if (it == opts.end() || it->second.metadata_only)
			return;
		std::string s = opt_json(it->second).dump();
		write_file(opts_path(k), s.data(), s.size());
	}

	void send_line(json const &j)
	{
		if (client_fd < 0)
			return;
		std::string s = j.dump(-1, ' ', false, json::error_handler_t::replace);
		s.push_back('\n');
		size_t off = 0;
		while (off < s.size())
		{
			ssize_t n = ::send(client_fd, s.data() + off, s.size() - off, MSG_NOSIGNAL);
			if (n < 0 && (errno == EAGAIN || errno == EINTR))
			{
				struct pollfd p = {client_fd, POLLOUT, 0};
				if (::poll(&p, 1, 2000) <= 0)
					break;
				continue;
			}
			if (n <= 0)
			{
				::close(client_fd);
				client_fd = -1;
				return;
			}
			off += size_t(n);
		}
	}

	void event(json j) { send_line(j); }

	// Settings -> settings_pack
	lt::settings_pack pack_from(json const &s)
	{
		lt::settings_pack p;
		int from = s.value("listen_from", 16891), to = s.value("listen_to", 16899);
		if (from <= 0 || from > 65535)
			from = 16891;
		if (to < from)
			to = from;
		p.set_str(lt::settings_pack::listen_interfaces, "0.0.0.0:" + std::to_string(from) + ",[::]:" + std::to_string(from));
		p.set_int(lt::settings_pack::max_retry_port_bind, to - from);
		bool dht = s.value("dht", true), lsd = s.value("lsd", true), upnp = s.value("upnp", false);
		bool utp = true, incoming = true;
		json px = s.value("proxy", json::object());
		std::string ptype = px.value("type", std::string("none"));
		bool force = px.value("force", true) && ptype != "none";
		if (ptype == "none" || px.value("host", std::string()).empty())
		{
			p.set_int(lt::settings_pack::proxy_type, lt::settings_pack::none);
		}
		else
		{
			bool auth = !px.value("user", std::string()).empty();
			int t = lt::settings_pack::none;
			if (ptype == "socks5")
				t = auth ? lt::settings_pack::socks5_pw : lt::settings_pack::socks5;
			else if (ptype == "http")
				t = auth ? lt::settings_pack::http_pw : lt::settings_pack::http;
			p.set_int(lt::settings_pack::proxy_type, t);
			p.set_str(lt::settings_pack::proxy_hostname, px.value("host", std::string()));
			p.set_int(lt::settings_pack::proxy_port, px.value("port", 0));
			p.set_str(lt::settings_pack::proxy_username, px.value("user", std::string()));
			p.set_str(lt::settings_pack::proxy_password, px.value("pass", std::string()));
			p.set_bool(lt::settings_pack::proxy_hostnames, px.value("hostnames", true));
			p.set_bool(lt::settings_pack::proxy_peer_connections, px.value("peers", true));
			p.set_bool(lt::settings_pack::proxy_tracker_connections, px.value("trackers", true));
			if (ptype == "http")
				utp = false; // an HTTP proxy cannot carry UDP
			if (force)
			{
				// Never connect directly: no incoming peers, no port mapping,
				// no local discovery, no UDP that might bypass the proxy
				incoming = false;
				upnp = false;
				lsd = false;
				dht = false;
				utp = false;
			}
		}
		p.set_bool(lt::settings_pack::enable_dht, dht);
		dht_on = dht;
		p.set_bool(lt::settings_pack::enable_lsd, lsd);
		p.set_bool(lt::settings_pack::enable_upnp, upnp);
		p.set_bool(lt::settings_pack::enable_natpmp, upnp);
		p.set_bool(lt::settings_pack::enable_outgoing_utp, utp);
		p.set_bool(lt::settings_pack::enable_incoming_utp, utp && incoming);
		p.set_bool(lt::settings_pack::enable_incoming_tcp, incoming);
		p.set_bool(lt::settings_pack::incoming_starts_queued_torrents, false);
		bool enc = s.value("encrypt", false);
		p.set_int(lt::settings_pack::out_enc_policy, enc ? lt::settings_pack::pe_forced : lt::settings_pack::pe_enabled);
		p.set_int(lt::settings_pack::in_enc_policy, enc ? lt::settings_pack::pe_forced : lt::settings_pack::pe_enabled);
		int conn = s.value("connections_limit", 300);
		if (conn > 0)
			p.set_int(lt::settings_pack::connections_limit, conn);
		p.set_int(lt::settings_pack::download_rate_limit, int(s.value("max_down", 0LL)));
		p.set_int(lt::settings_pack::upload_rate_limit, int(s.value("max_up", 0LL)));
		std::string fp = s.value("peer_id_prefix", std::string());
		if (fp.size() == 8)
			p.set_str(lt::settings_pack::peer_fingerprint, fp);
		else
			p.set_str(lt::settings_pack::peer_fingerprint, "-DC1000-");
		std::string ua = s.value("user_agent", std::string());
		p.set_str(lt::settings_pack::user_agent, ua.empty() ? std::string("libtorrent/") + LIBTORRENT_VERSION : ua);
		p.set_str(lt::settings_pack::dht_bootstrap_nodes, s.value("dht_bootstrap_nodes",
			std::string("dht.libtorrent.org:25401,router.bittorrent.com:6881,router.utorrent.com:6881,dht.transmissionbt.com:6881,router.bitcomet.com:6881")));
		// Trackers added to a torrent (by hand, the public list, another
		// link) are announced to together, not one after the other
		p.set_bool(lt::settings_pack::announce_to_all_tiers, true);
		p.set_bool(lt::settings_pack::announce_to_all_trackers, true);
		// dcd does the queueing
		p.set_int(lt::settings_pack::active_downloads, -1);
		p.set_int(lt::settings_pack::active_seeds, -1);
		p.set_int(lt::settings_pack::active_limit, -1);
		p.set_int(lt::settings_pack::active_checking, 2);
		p.set_int(lt::settings_pack::alert_mask, lt::alert_category::status | lt::alert_category::error | lt::alert_category::storage);
		p.set_int(lt::settings_pack::alert_queue_size, 10000);
		// The default (peer_proportional) caps TCP peers at a multiple of
		// their share whenever some uTP peers are downloading too, which
		// slows the ramp-up; uTP backs off by itself when TCP fills the line
		p.set_int(lt::settings_pack::mixed_mode_algorithm, lt::settings_pack::prefer_tcp);
		p.set_int(lt::settings_pack::hashing_threads, tune.hashing_threads);
		p.set_int(lt::settings_pack::max_queued_disk_bytes, tune.max_queued_disk_bytes);
		p.set_int(lt::settings_pack::checking_mem_usage, tune.checking_mem_usage);
		return p;
	}

	void start(json const &initial)
	{
		settings = initial;
		lt::session_params sp;
		std::vector<char> st;
		if (read_file(state_dir + "/session.state", st) && !st.empty())
		{
			try
			{
				sp = lt::read_session_params(st, lt::session_handle::save_dht_state);
			}
			catch (...)
			{
				sp = lt::session_params();
			}
		}
		sp.settings = pack_from(initial);
		// The extensions are added below: peer exchange only when it is on.
		// It is a session plugin, so changing it needs a restart (apply_settings)
		sp.extensions.clear();
		ses.reset(new lt::session(std::move(sp)));
		ses->add_extension(&lt::create_ut_metadata_plugin);
		ses->add_extension(&lt::create_smart_ban_plugin);
		ses->add_extension(std::make_shared<FoundSession>());
		if (initial.value("pex", true))
			ses->add_extension(&lt::create_ut_pex_plugin);
		update_guard(true);
		dht_next = clk::now() + std::chrono::minutes(2);
		restore();
	}

	void restore()
	{
		DIR *d = opendir(state_dir.c_str());
		if (!d)
			return;
		struct dirent *e;
		std::vector<std::string> names;
		while ((e = readdir(d)) != nullptr)
		{
			std::string n = e->d_name;
			if (n.size() > 7 && n.compare(n.size() - 7, 7, ".resume") == 0)
				names.push_back(n.substr(0, n.size() - 7));
		}
		closedir(d);
		for (auto &k : names)
		{
			std::vector<char> buf;
			if (!read_file(resume_path(k), buf))
				continue;
			lt::error_code ec;
			lt::add_torrent_params atp = lt::read_resume_data(buf, ec);
			if (ec)
			{
				dlog("resume %s: %s", k.c_str(), ec.message().c_str());
				continue;
			}
			Options o;
			std::vector<char> ob;
			if (read_file(opts_path(k), ob))
			{
				try
				{
					o = opt_from(json::parse(ob.begin(), ob.end()));
				}
				catch (...)
				{
				}
			}
			atp.flags &= ~lt::torrent_flags::auto_managed;
			// Saved before 1.4, without guard: whose torrent it is shows when
			// dcd adds it again, so it waits for that instead of running unguarded
			if (o.complete || (!o.guard_known && !o.metadata_only))
				atp.flags |= lt::torrent_flags::paused;
			// Resume data keeps the flag; the options say what it must be
			if (o.guard)
				atp.flags |= lt::torrent_flags::apply_ip_filter;
			else
				atp.flags &= ~lt::torrent_flags::apply_ip_filter;
			atp.userdata = lt::client_data_t(found_for(k));
			lt::torrent_handle h = ses->add_torrent(std::move(atp), ec);
			if (ec)
			{
				dlog("restore %s: %s", k.c_str(), ec.message().c_str());
				continue;
			}
			if (o.guard)
				strip_forbidden(h);
			std::string key = key_of(h.info_hashes());
			handles[key] = h;
			opts[key] = o;
		}
		dlog("restored %d torrents", int(handles.size()));
	}

	lt::torrent_handle find(json const &req)
	{
		std::string k = req.value("infohash", std::string());
		auto it = handles.find(k);
		if (it == handles.end() || !it->second.is_valid())
			throw std::runtime_error("torrent not found");
		return it->second;
	}

	void apply_priorities(lt::torrent_handle const &h, Options const &o)
	{
		auto ti = h.torrent_file();
		if (!ti)
			return;
		int n = ti->num_files();
		std::vector<lt::download_priority_t> pr(size_t(n), o.has_select ? lt::dont_download : lt::default_priority);
		if (o.has_select)
			for (int i : o.select)
				if (i >= 0 && i < n)
					pr[size_t(i)] = lt::default_priority;
		for (auto &kv : o.priorities)
			if (kv.first >= 0 && kv.first < n)
				pr[size_t(kv.first)] = lt::download_priority_t(std::uint8_t(std::max(0, std::min(7, kv.second))));
		h.prioritize_files(pr);
	}

	static void read_select(json const &req, Options &o)
	{
		if (req.contains("select") && req["select"].is_array())
		{
			o.has_select = true;
			o.select.clear();
			for (auto &v : req["select"])
				if (v.is_number_integer())
					o.select.push_back(v.get<int>());
		}
		if (req.contains("priorities") && req["priorities"].is_object())
		{
			o.priorities.clear();
			for (auto it = req["priorities"].begin(); it != req["priorities"].end(); ++it)
				if (it.value().is_number_integer())
					o.priorities[atoi(it.key().c_str())] = it.value().get<int>();
		}
	}

	json cmd_add(json const &req)
	{
		lt::add_torrent_params atp;
		lt::error_code ec;
		std::shared_ptr<lt::torrent_info> ti;
		std::string b64 = req.value("torrent_b64", std::string());
		std::string magnet = req.value("magnet", std::string());
		if (!b64.empty())
		{
			std::vector<char> raw = b64decode(b64);
			ti = std::make_shared<lt::torrent_info>(lt::span<char const>(raw.data(), long(raw.size())), ec, lt::from_span);
			if (ec)
				throw std::runtime_error("invalid torrent: " + ec.message());
			atp.ti = ti;
		}
		else if (!magnet.empty())
		{
			lt::parse_magnet_uri(magnet, atp, ec);
			if (ec)
				throw std::runtime_error("invalid magnet: " + ec.message());
		}
		else
			throw std::runtime_error("torrent_b64 or magnet required");

		std::string key = ti ? key_of(ti->info_hashes()) : key_of(atp.info_hashes);
		std::string save = req.value("save_path", std::string());
		if (save.empty())
			throw std::runtime_error("save_path required");

		Options o;
		o.seed_ratio = req.value("seed_ratio", 0.0);
		o.seed_time = req.value("seed_time", 0);
		o.metadata_only = req.value("metadata_only", false);
		o.guard = req.value("guard", false);
		read_select(req, o);

		auto ex = handles.find(key);
		if (ex != handles.end() && ex->second.is_valid() && opts[key].metadata_only && !o.metadata_only)
		{
			// A pending file-list probe of the same torrent: the real task wins
			ses->remove_torrent(ex->second);
			forget(key);
			ex = handles.end();
		}
		if (ex != handles.end() && ex->second.is_valid())
		{
			// Already in the session (restored from resume data): keep it
			for (auto &t : req.value("trackers", json::array()))
				if (t.is_string())
					ex->second.add_tracker(lt::announce_entry(t.get<std::string>()));
			// A probe never changes the guard of the task it found
			Options &eo = opts[key];
			if (!o.metadata_only && (eo.guard != o.guard || !eo.guard_known))
			{
				eo.guard = o.guard;
				eo.guard_known = true;
				save_opts(key);
			}
			set_guard(ex->second, eo.guard);
			return json{{"infohash", key}, {"existed", true}, {"resumed", true}};
		}

		bool resumed = false;
		std::string fr = req.value("fastresume", std::string());
		std::vector<char> buf;
		bool check = req.value("check", false);
		if (ti && !fr.empty() && read_file(fr, buf))
		{
			lt::error_code rec;
			lt::add_torrent_params r = lt::read_resume_data(buf, rec);
			if (!rec && (r.info_hashes == ti->info_hashes() || r.info_hashes.v1 == ti->info_hashes().v1 || r.info_hashes.v1.is_all_zeros()))
			{
				r.ti = ti;
				atp = std::move(r);
				resumed = true;
				check = false;
			}
			else
				dlog("fastresume %s not usable: %s", key.c_str(), rec ? rec.message().c_str() : "info hash mismatch");
		}
		if (!resumed && !check && read_file(resume_path(key), buf))
		{
			lt::error_code rec;
			lt::add_torrent_params r = lt::read_resume_data(buf, rec);
			if (!rec)
			{
				if (ti)
					r.ti = ti;
				atp = std::move(r);
				resumed = true;
			}
		}
		atp.save_path = save;
		std::string root = req.value("root", std::string());
		if (ti && !root.empty())
			for (auto &kv : root_renames(ti->orig_files(), root))
				atp.renamed_files[kv.first] = kv.second;
		atp.flags &= ~lt::torrent_flags::auto_managed;
		atp.flags &= ~lt::torrent_flags::duplicate_is_error;
		if (req.value("paused", false))
			atp.flags |= lt::torrent_flags::paused;
		else
			atp.flags &= ~lt::torrent_flags::paused;
		if (req.value("sequential", false))
			atp.flags |= lt::torrent_flags::sequential_download;
		if (o.guard)
			atp.flags |= lt::torrent_flags::apply_ip_filter;
		else
			atp.flags &= ~lt::torrent_flags::apply_ip_filter;
		if (o.metadata_only)
		{
			atp.flags &= ~lt::torrent_flags::paused;
			atp.flags |= lt::torrent_flags::upload_mode;
			o.magnet_save = save;
		}
		for (auto &t : req.value("trackers", json::array()))
			if (t.is_string())
				atp.trackers.push_back(t.get<std::string>());
		long long md = req.value("max_down", 0LL), mu = req.value("max_up", 0LL);
		if (md > 0)
			atp.download_limit = int(md);
		if (mu > 0)
			atp.upload_limit = int(mu);
		int mp = req.value("max_peers", 0);
		if (mp > 0)
			atp.max_connections = mp;
		if (ti && !resumed && (o.has_select || !o.priorities.empty()))
		{
			int n = ti->num_files();
			atp.file_priorities.assign(size_t(n), o.has_select ? lt::dont_download : lt::default_priority);
			if (o.has_select)
				for (int i : o.select)
					if (i >= 0 && i < n)
						atp.file_priorities[size_t(i)] = lt::default_priority;
			for (auto &kv : o.priorities)
				if (kv.first >= 0 && kv.first < n)
					atp.file_priorities[size_t(kv.first)] = lt::download_priority_t(std::uint8_t(std::max(0, std::min(7, kv.second))));
		}
		atp.userdata = lt::client_data_t(found_for(key));
		lt::torrent_handle h = ses->add_torrent(std::move(atp), ec);
		if (ec)
		{
			if (!handles.count(key))
				forget_found(key);
			throw std::runtime_error(ec.message());
		}
		if (o.guard)
			strip_forbidden(h);
		if (check)
			h.force_recheck();
		if (resumed && (o.has_select || !o.priorities.empty()))
			apply_priorities(h, o);
		handles[key] = h;
		opts[key] = o;
		save_opts(key);
		if (!o.metadata_only)
			h.save_resume_data(lt::torrent_handle::save_info_dict);
		return json{{"infohash", key}, {"existed", false}, {"resumed", resumed}};
	}

	// Move the data to another folder; the torrent keeps running and reads
	// from there once libtorrent is done (storage_moved). Nothing that already
	// exists at the destination is replaced: the move fails instead.
	json cmd_move(json const &req)
	{
		lt::torrent_handle h = find(req);
		std::string k = req.value("infohash", std::string());
		std::string dst = req.value("save_path", std::string());
		if (dst.empty())
			throw std::runtime_error("save_path required");
		auto ti = h.torrent_file();
		if (!ti)
			throw std::runtime_error("no metadata yet");
		std::string root = req.value("root", std::string());
		if (!root.empty())
		{
			lt::file_storage const &cur = ti->files();
			for (auto &kv : root_renames(ti->orig_files(), root))
				if (cur.file_path(kv.first) != kv.second)
					h.rename_file(kv.first, kv.second);
		}
		opts[k].move_error.clear();
		h.move_storage(dst, lt::move_flags_t::fail_if_exist);
		return json::object();
	}

	void forget(std::string const &k)
	{
		handles.erase(k);
		opts.erase(k);
		forget_found(k);
		::unlink(resume_path(k).c_str());
		::unlink(opts_path(k).c_str());
	}

	json status_of(std::string const &k, lt::torrent_handle const &h, bool files)
	{
		lt::torrent_status s = h.status(lt::torrent_handle::query_pieces | lt::torrent_handle::query_name | lt::torrent_handle::query_save_path | lt::torrent_handle::query_distributed_copies);
		Options &o = opts[k];
		json j;
		j["infohash"] = k;
		j["name"] = s.name;
		j["save_path"] = s.save_path;
		j["state"] = state_name(s.state);
		j["paused"] = bool(s.flags & lt::torrent_flags::paused);
		j["complete"] = o.complete;
		j["moving"] = s.moving_storage;
		j["move_error"] = o.move_error;
		j["has_metadata"] = s.has_metadata;
		j["error"] = s.errc ? s.errc.message() : std::string();
		j["total_wanted"] = s.total_wanted;
		j["total_wanted_done"] = s.total_wanted_done;
		j["all_time_upload"] = s.all_time_upload;
		j["all_time_download"] = s.all_time_download;
		j["down_rate"] = s.download_payload_rate;
		j["up_rate"] = s.upload_payload_rate;
		j["peers"] = s.num_peers;
		j["seeds"] = s.num_seeds;
		j["known_peers"] = s.list_peers;
		j["connect_candidates"] = s.connect_candidates;
		j["sequential"] = bool(s.flags & lt::torrent_flags::sequential_download);
		j["seeding_secs"] = (long long)(s.seeding_duration.count());
		j["finished_secs"] = (long long)(s.finished_duration.count());
		// How many complete copies the connected peers have together
		j["availability"] = s.has_metadata ? double(s.distributed_copies) : -1.0;
		j["last_seen_complete"] = (long long)s.last_seen_complete;
		auto ti = h.torrent_file();
		// libtorrent announces a running torrent to DHT every 15 minutes
		// while DHT has nodes; private torrents never use it
		j["dht_up"] = dht_up && dht_on && !(ti && ti->priv());
		// Peers found without the NAS itself (see FoundPlugin)
		auto fi = found.find(k);
		if (fi != found.end())
		{
			std::lock_guard<std::mutex> l(fi->second->mu);
			j["found_peers"] = fi->second->all.size();
			j["dht_peers"] = fi->second->dht.size();
			j["dht_reply_at"] = fi->second->dht_at;
		}
		else
		{
			j["found_peers"] = 0;
			j["dht_peers"] = 0;
			j["dht_reply_at"] = 0LL;
		}
		if (ti)
		{
			j["pieces"] = bits_hex(s.pieces);
			j["num_pieces"] = ti->num_pieces();
			j["piece_length"] = ti->piece_length();
			j["comment"] = ti->comment();
			j["private"] = ti->priv();
			if (files)
			{
				std::vector<std::int64_t> prog;
				h.file_progress(prog, lt::torrent_handle::piece_granularity);
				std::vector<lt::download_priority_t> pr = h.get_file_priorities();
				json fa = json::array();
				lt::file_storage const &fs = ti->files();
				for (lt::file_index_t i : fs.file_range())
				{
					int idx = int(static_cast<int>(i));
					json f;
					f["index"] = idx;
					f["path"] = fs.file_path(i);
					f["size"] = fs.file_size(i);
					f["done"] = size_t(idx) < prog.size() ? prog[size_t(idx)] : 0;
					f["priority"] = size_t(idx) < pr.size() ? int(static_cast<std::uint8_t>(pr[size_t(idx)])) : 4;
					f["pad"] = fs.pad_file_at(i);
					fa.push_back(f);
				}
				j["files"] = fa;
			}
		}
		return j;
	}

	// Write the .torrent of a torrent whose metadata just arrived.
	void write_metadata(std::string const &k, lt::torrent_handle const &h, std::string const &dir)
	{
		auto ti = h.torrent_file();
		if (!ti || dir.empty())
			return;
		std::string path = dir + "/" + k + ".torrent";
		struct stat st;
		if (::stat(path.c_str(), &st) == 0)
			return;
		lt::span<char const> info = ti->info_section();
		std::string out = "d";
		std::vector<lt::announce_entry> tr = h.trackers();
		if (!tr.empty())
		{
			out += "13:announce-listl";
			for (auto &a : tr)
				out += "l" + std::to_string(a.url.size()) + ":" + a.url + "e";
			out += "e";
		}
		out += "4:info";
		out.append(info.data(), size_t(info.size()));
		out += "e";
		write_file(path, out.data(), out.size());
	}

	json cmd_status_all(json const &req)
	{
		bool files = req.value("files", true);
		json arr = json::array();
		for (auto &kv : handles)
		{
			if (!kv.second.is_valid() || opts[kv.first].metadata_only)
				continue;
			try
			{
				arr.push_back(status_of(kv.first, kv.second, files));
			}
			catch (std::exception const &e)
			{
				dlog("status %s: %s", kv.first.c_str(), e.what());
			}
		}
		return json{{"torrents", arr}};
	}

	// Replace every tracker of a torrent (a magnet that turned out to be a
	// private torrent keeps only its own trackers).
	json cmd_set_trackers(json const &req)
	{
		lt::torrent_handle h = find(req);
		std::string k = req.value("infohash", std::string());
		std::vector<lt::announce_entry> tr;
		for (auto &t : req.value("trackers", json::array()))
			if (t.is_string())
			{
				lt::announce_entry e(t.get<std::string>());
				e.tier = std::uint8_t(std::min<size_t>(tr.size(), 255));
				tr.push_back(e);
			}
		h.replace_trackers(tr);
		if (opts[k].guard)
			strip_forbidden(h);
		if (h.torrent_file())
		{
			// The saved .torrent lists the trackers it was written with
			::unlink((torrents_dir + "/" + k + ".torrent").c_str());
			write_metadata(k, h, torrents_dir);
			h.save_resume_data(lt::torrent_handle::save_info_dict);
		}
		if (!tr.empty())
			h.force_reannounce(0, -1, lt::torrent_handle::ignore_min_interval);
		return json::object();
	}

	// Forget the peers of a torrent that has no connection and look for
	// peers again: peers that failed three times are never tried again
	// otherwise (libtorrent's max_failcount), while the torrent runs.
	json cmd_refresh_peers(json const &req)
	{
		lt::torrent_handle h = find(req);
		lt::torrent_status s = h.status(lt::status_flags_t{});
		if (s.num_peers > 0)
			return json{{"refreshed", false}};
		h.clear_peers();
		auto ti = h.torrent_file();
		if (!(ti && ti->priv()))
			h.force_dht_announce();
		h.force_reannounce(0, -1, lt::torrent_handle::ignore_min_interval);
		return json{{"refreshed", true}};
	}

	// Every tracker of a torrent as libtorrent knows it from its announces,
	// for dcd's source health; nothing is sent to the trackers. A tracker
	// has an endpoint per listen socket and an entry per info hash (v1, v2):
	// one that answered counts (fewest failures), counts are the largest
	// known (-1 unknown) and the message is the failure reason of a failing
	// tracker, or the warning of a working one.
	json cmd_sources(json const &req)
	{
		lt::torrent_handle h = find(req);
		json arr = json::array();
		for (lt::announce_entry const &ae : h.trackers())
		{
			int seeds = -1, leechers = -1, fails = -1;
			bool updating = false;
			std::string warning, failure;
			for (auto const &ep : ae.endpoints)
				for (auto const &ih : ep.info_hashes)
				{
					seeds = std::max(seeds, ih.scrape_complete);
					leechers = std::max(leechers, ih.scrape_incomplete);
					updating = updating || ih.updating;
					if (ih.fails == 0 && !ih.start_sent)
						continue; // not tried yet
					if (fails < 0 || int(ih.fails) < fails)
						fails = int(ih.fails);
					if (ih.fails == 0)
					{
						if (warning.empty())
							warning = ih.message;
					}
					else if (failure.empty())
						failure = !ih.message.empty() ? ih.message : ih.last_error ? ih.last_error.message() : std::string();
				}
			json t;
			t["url"] = ae.url;
			t["tier"] = int(ae.tier);
			t["contacted"] = fails >= 0;
			t["fails"] = std::max(fails, 0);
			t["message"] = fails > 0 ? failure : warning;
			t["seeds"] = seeds;
			t["leechers"] = leechers;
			t["updating"] = updating;
			arr.push_back(t);
		}
		return json{{"trackers", arr}};
	}

	json cmd_peers(json const &req)
	{
		lt::torrent_handle h = find(req);
		std::vector<lt::peer_info> peers;
		h.get_peer_info(peers);
		json arr = json::array();
		for (auto &p : peers)
		{
			json j;
			j["ip"] = p.ip.address().to_string();
			j["port"] = p.ip.port();
			j["client"] = p.client;
			j["down_rate"] = p.payload_down_speed;
			j["up_rate"] = p.payload_up_speed;
			j["seed"] = bool(p.flags & lt::peer_info::seed);
			j["progress"] = p.progress;
			arr.push_back(j);
		}
		return json{{"peers", arr}};
	}

	int save_all(int timeout_ms)
	{
		int n = 0;
		for (auto &kv : handles)
		{
			if (!kv.second.is_valid() || opts[kv.first].metadata_only)
				continue;
			if (!kv.second.status(lt::status_flags_t{}).has_metadata)
				continue;
			kv.second.save_resume_data(lt::torrent_handle::save_info_dict | lt::torrent_handle::flush_disk_cache);
			++n;
		}
		pending_saves += n;
		auto deadline = clk::now() + std::chrono::milliseconds(timeout_ms);
		while (pending_saves > 0 && clk::now() < deadline)
		{
			ses->wait_for_alert(std::chrono::milliseconds(200));
			handle_alerts();
		}
		pending_saves = 0;
		save_session();
		return n;
	}

	void save_session()
	{
		std::vector<char> b = lt::write_session_params_buf(ses->session_state(lt::session_handle::save_dht_state), lt::session_handle::save_dht_state);
		write_file(state_dir + "/session.state", b.data(), b.size());
	}

	json dispatch(json const &req)
	{
		std::string cmd = req.value("cmd", std::string());
		if (cmd == "version")
			return json{{"dcbt", DCBT_VERSION}, {"libtorrent", LIBTORRENT_VERSION}, {"hardware", hardware_json(hw, tune)}};
		if (cmd == "apply_settings")
		{
			json next = req.value("settings", req);
			// Peer exchange is chosen when the session starts: dcd restarts dc-bt
			if (next.value("pex", true) != settings.value("pex", true))
				throw std::runtime_error("restart required");
			settings = next;
			ses->apply_settings(pack_from(settings));
			if (!dht_on)
				dht_up = false;
			update_guard(false);
			return json::object();
		}
		if (cmd == "add")
			return cmd_add(req);
		if (cmd == "status_all")
			return cmd_status_all(req);
		if (cmd == "status")
		{
			lt::torrent_handle h = find(req);
			return status_of(req.value("infohash", std::string()), h, true);
		}
		if (cmd == "pause")
		{
			lt::torrent_handle h = find(req);
			h.unset_flags(lt::torrent_flags::auto_managed);
			h.pause(lt::torrent_handle::graceful_pause);
			return json::object();
		}
		if (cmd == "resume")
		{
			lt::torrent_handle h = find(req);
			h.unset_flags(lt::torrent_flags::auto_managed | lt::torrent_flags::upload_mode);
			h.resume();
			return json::object();
		}
		if (cmd == "remove")
		{
			std::string k = req.value("infohash", std::string());
			auto it = handles.find(k);
			if (it != handles.end() && it->second.is_valid())
				ses->remove_torrent(it->second);
			forget(k);
			return json::object();
		}
		if (cmd == "force_recheck")
		{
			find(req).force_recheck();
			return json::object();
		}
		if (cmd == "move")
			return cmd_move(req);
		if (cmd == "set_file_priorities")
		{
			lt::torrent_handle h = find(req);
			std::string k = req.value("infohash", std::string());
			Options &o = opts[k];
			read_select(req, o);
			apply_priorities(h, o);
			if (o.complete)
				o.complete = false; // more data wanted: dcd resumes it
			save_opts(k);
			return json::object();
		}
		if (cmd == "set_limits")
		{
			lt::torrent_handle h = find(req);
			h.set_download_limit(int(req.value("down", 0LL)));
			h.set_upload_limit(int(req.value("up", 0LL)));
			return json::object();
		}
		if (cmd == "set_sequential")
		{
			lt::torrent_handle h = find(req);
			if (req.value("on", false))
				h.set_flags(lt::torrent_flags::sequential_download);
			else
				h.unset_flags(lt::torrent_flags::sequential_download);
			return json::object();
		}
		if (cmd == "set_seeding")
		{
			find(req);
			std::string k = req.value("infohash", std::string());
			Options &o = opts[k];
			o.seed_ratio = req.value("seed_ratio", 0.0);
			o.seed_time = req.value("seed_time", 0);
			o.complete = false;
			save_opts(k);
			return json::object();
		}
		if (cmd == "add_trackers")
		{
			lt::torrent_handle h = find(req);
			for (auto &t : req.value("trackers", json::array()))
				if (t.is_string())
					h.add_tracker(lt::announce_entry(t.get<std::string>()));
			Options const &o = opts[req.value("infohash", std::string())];
			if (o.guard)
				strip_forbidden(h);
			h.force_reannounce();
			// Saved now: a magnet without its file list is left out when
			// dc-bt stops, and the periodic save may not come first
			if (!o.metadata_only)
				h.save_resume_data(lt::torrent_handle::save_info_dict);
			return json::object();
		}
		if (cmd == "set_trackers")
			return cmd_set_trackers(req);
		if (cmd == "set_guard")
		{
			// The owner's role changed, or a torrent got the public tracker list
			lt::torrent_handle h = find(req);
			std::string k = req.value("infohash", std::string());
			Options &o = opts[k];
			o.guard = req.value("on", false);
			o.guard_known = true;
			save_opts(k);
			set_guard(h, o.guard);
			return json::object();
		}
		if (cmd == "refresh_peers")
			return cmd_refresh_peers(req);
		if (cmd == "sources")
			return cmd_sources(req);
		if (cmd == "trackers")
		{
			json arr = json::array();
			for (auto &a : find(req).trackers())
				arr.push_back(a.url);
			return json{{"trackers", arr}};
		}
		if (cmd == "peers")
			return cmd_peers(req);
		if (cmd == "save_state")
			return json{{"saved", save_all(10000)}};
		if (cmd == "shutdown")
		{
			g_stop = 1;
			return json::object();
		}
		throw std::runtime_error("unknown command: " + cmd);
	}

	// Seeding targets are per torrent here (libtorrent's are session wide).
	void check_seeding()
	{
		for (auto &kv : handles)
		{
			Options &o = opts[kv.first];
			if (o.complete || o.metadata_only || !kv.second.is_valid())
				continue;
			lt::torrent_status s = kv.second.status(lt::status_flags_t{});
			if (s.flags & lt::torrent_flags::paused)
				continue;
			if (s.state != lt::torrent_status::seeding && s.state != lt::torrent_status::finished)
				continue;
			bool done = false;
			if (o.seed_time < 0)
				done = true;
			else
			{
				std::int64_t base = std::max(s.all_time_download, s.total_wanted_done);
				double ratio = base > 0 ? double(s.all_time_upload) / double(base) : 0;
				if (o.seed_time > 0 && s.finished_duration.count() >= o.seed_time * 60)
					done = true;
				if (o.seed_ratio > 0 && ratio >= o.seed_ratio)
					done = true;
			}
			if (done)
			{
				o.complete = true;
				kv.second.unset_flags(lt::torrent_flags::auto_managed);
				kv.second.pause(lt::torrent_handle::graceful_pause);
				save_opts(kv.first);
				kv.second.save_resume_data(lt::torrent_handle::save_info_dict);
				event(json{{"event", "seeding_complete"}, {"infohash", kv.first}});
			}
		}
	}

	void handle_alerts()
	{
		std::vector<lt::alert *> alerts;
		ses->pop_alerts(&alerts);
		for (lt::alert *a : alerts)
		{
			if (auto *r = lt::alert_cast<lt::save_resume_data_alert>(a))
			{
				std::string k = key_of(r->handle.info_hashes());
				if (handles.count(k) && !opts[k].metadata_only)
				{
					std::vector<char> b = lt::write_resume_data_buf(r->params);
					write_file(resume_path(k), b.data(), b.size());
					event(json{{"event", "resume_saved"}, {"infohash", k}});
				}
				if (pending_saves > 0)
					--pending_saves;
			}
			else if (lt::alert_cast<lt::save_resume_data_failed_alert>(a))
			{
				if (pending_saves > 0)
					--pending_saves;
			}
			else if (auto *m = lt::alert_cast<lt::metadata_received_alert>(a))
			{
				std::string k = key_of(m->handle.info_hashes());
				Options &o = opts[k];
				if (o.metadata_only)
				{
					write_metadata(k, m->handle, o.magnet_save);
					ses->remove_torrent(m->handle);
					forget(k);
				}
				else
				{
					write_metadata(k, m->handle, torrents_dir);
					if (o.has_select || !o.priorities.empty())
						apply_priorities(m->handle, o);
					m->handle.save_resume_data(lt::torrent_handle::save_info_dict);
				}
				event(json{{"event", "metadata_received"}, {"infohash", k}});
			}
			else if (auto *f = lt::alert_cast<lt::torrent_finished_alert>(a))
			{
				std::string k = key_of(f->handle.info_hashes());
				f->handle.save_resume_data(lt::torrent_handle::save_info_dict);
				event(json{{"event", "torrent_finished"}, {"infohash", k}});
			}
			else if (auto *mv = lt::alert_cast<lt::storage_moved_alert>(a))
			{
				std::string k = key_of(mv->handle.info_hashes());
				auto it = opts.find(k);
				if (it != opts.end())
					it->second.move_error.clear();
				// The new save path must survive a restart right away
				mv->handle.save_resume_data(lt::torrent_handle::save_info_dict);
				event(json{{"event", "storage_moved"}, {"infohash", k}, {"save_path", mv->storage_path()}});
			}
			else if (auto *mf = lt::alert_cast<lt::storage_moved_failed_alert>(a))
			{
				std::string k = key_of(mf->handle.info_hashes());
				std::string msg = mf->error.message();
				char const *file = mf->file_path();
				if (file && *file)
					msg += std::string(": ") + file;
				auto it = opts.find(k);
				if (it != opts.end())
					it->second.move_error = msg;
				dlog("move %s failed: %s", k.c_str(), msg.c_str());
				event(json{{"event", "storage_move_failed"}, {"infohash", k}, {"error", msg}});
			}
			else if (auto *e = lt::alert_cast<lt::torrent_error_alert>(a))
			{
				event(json{{"event", "torrent_error"}, {"infohash", key_of(e->handle.info_hashes())}, {"error", e->error.message()}});
			}
			else if (auto *s = lt::alert_cast<lt::state_changed_alert>(a))
			{
				event(json{{"event", "state_changed"}, {"infohash", key_of(s->handle.info_hashes())}, {"state", state_name(s->state)}});
			}
			else if (auto *l = lt::alert_cast<lt::listen_failed_alert>(a))
			{
				dlog("listen failed: %s", l->message().c_str());
				event(json{{"event", "listen_failed"}, {"error", l->message()}});
			}
			else if (auto *ls = lt::alert_cast<lt::listen_succeeded_alert>(a))
			{
				bool fresh;
				{
					std::lock_guard<std::mutex> l(self_mu);
					fresh = self_ports.insert(ls->port).second;
				}
				if (fresh)
					set_self();
				dlog("%s", ls->message().c_str());
			}
			else if (auto *x = lt::alert_cast<lt::external_ip_alert>(a))
			{
				lt::address ip = plain(x->external_address);
				if (std::find(external.begin(), external.end(), ip) == external.end())
				{
					dlog("external address: %s", ip.to_string().c_str());
					external.push_back(ip);
					if (external.size() > 8)
						external.erase(external.begin());
					set_self();
				}
			}
			else if (auto *ss = lt::alert_cast<lt::session_stats_alert>(a))
			{
				static int const idx = lt::find_metric_idx("dht.dht_nodes");
				if (dht_asked && idx >= 0)
					on_dht_nodes(ss->counters()[idx]);
			}
		}
	}

	// announce_dht runs when DHT has nodes for the first time, or again after
	// bootstrap retries: a torrent added or resumed before that (dcd adds
	// every task right after dc-bt starts) asked a DHT without nodes, found
	// nobody, and libtorrent asks again only on its round, up to 15 minutes
	// later. Running torrents without a connection ask now.
	void announce_dht()
	{
		int n = 0;
		for (auto &kv : handles)
		{
			if (!kv.second.is_valid())
				continue;
			lt::torrent_status s = kv.second.status(lt::status_flags_t{});
			if ((s.flags & lt::torrent_flags::paused) || s.num_peers > 0 || s.is_seeding)
				continue;
			kv.second.force_dht_announce();
			++n;
		}
		if (n > 0)
			dlog("DHT is up: asking it for the peers of %d torrents", n);
	}

	// check_dht asks for the session counters to see whether DHT has nodes.
	// libtorrent resolves the bootstrap nodes once, when DHT starts, and does
	// not retry when that fails (no network yet when the NAS boots); turning
	// DHT off and on again resolves them again.
	void check_dht(clk::time_point now)
	{
		if (now < dht_next)
			return;
		if (!ses->get_settings().get_bool(lt::settings_pack::enable_dht))
		{
			dht_asked = false;
			dht_up = false;
			dht_tries = 0;
			dht_next = now + std::chrono::minutes(5);
			return;
		}
		// Asked again a minute later if the answer never comes
		dht_asked = true;
		dht_next = now + std::chrono::minutes(1);
		ses->post_session_stats();
	}

	void on_dht_nodes(std::int64_t nodes)
	{
		dht_asked = false;
		auto now = clk::now();
		if (nodes > 0 || !ses->get_settings().get_bool(lt::settings_pack::enable_dht))
		{
			if (nodes > 0 && !dht_up)
				announce_dht();
			dht_up = nodes > 0;
			dht_tries = 0;
			dht_next = now + std::chrono::minutes(5);
			return;
		}
		dht_up = false;
		static const int waits[] = {5, 10, 20, 30};
		dht_next = now + std::chrono::minutes(waits[std::min(dht_tries, 3)]);
		++dht_tries;
		dlog("DHT has no nodes: looking up the bootstrap nodes again (try %d)", dht_tries);
		lt::settings_pack off, on;
		off.set_bool(lt::settings_pack::enable_dht, false);
		on.set_bool(lt::settings_pack::enable_dht, true);
		ses->apply_settings(off);
		ses->apply_settings(on);
	}

	void on_line(std::string const &line)
	{
		json req;
		try
		{
			req = json::parse(line);
		}
		catch (std::exception const &e)
		{
			send_line(json{{"ok", false}, {"error", std::string("bad json: ") + e.what()}});
			return;
		}
		json resp;
		resp["id"] = req.value("id", 0LL);
		try
		{
			resp["result"] = dispatch(req);
			resp["ok"] = true;
		}
		catch (std::exception const &e)
		{
			resp["ok"] = false;
			resp["error"] = e.what();
		}
		send_line(resp);
	}

	bool listen_socket()
	{
		::unlink(socket_path.c_str());
		listen_fd = ::socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
		if (listen_fd < 0)
			return false;
		struct sockaddr_un addr;
		memset(&addr, 0, sizeof addr);
		addr.sun_family = AF_UNIX;
		if (socket_path.size() >= sizeof addr.sun_path)
			return false;
		strcpy(addr.sun_path, socket_path.c_str());
		mode_t old = umask(0177);
		int r = ::bind(listen_fd, (struct sockaddr *)&addr, sizeof addr);
		umask(old);
		if (r < 0 || ::listen(listen_fd, 4) < 0)
			return false;
		::chmod(socket_path.c_str(), 0600);
		return true;
	}

	void run()
	{
		auto last_seed = clk::now(), last_save = clk::now(), last_guard = clk::now();
		while (!g_stop)
		{
			struct pollfd fds[2];
			int n = 0;
			fds[n++] = {listen_fd, POLLIN, 0};
			if (client_fd >= 0)
				fds[n++] = {client_fd, POLLIN, 0};
			int r = ::poll(fds, nfds_t(n), 100);
			if (r > 0)
			{
				if (fds[0].revents & POLLIN)
				{
					int c = ::accept4(listen_fd, nullptr, nullptr, SOCK_CLOEXEC);
					if (c >= 0)
					{
						if (client_fd >= 0)
							::close(client_fd);
						client_fd = c;
						inbuf.clear();
					}
				}
				if (n > 1 && client_fd >= 0 && (fds[1].revents & (POLLIN | POLLHUP | POLLERR)))
				{
					char buf[65536];
					ssize_t got = ::recv(client_fd, buf, sizeof buf, 0);
					if (got <= 0)
					{
						::close(client_fd);
						client_fd = -1;
						inbuf.clear();
					}
					else
					{
						inbuf.append(buf, size_t(got));
						if (inbuf.size() > (64u << 20))
						{
							::close(client_fd);
							client_fd = -1;
							inbuf.clear();
						}
						size_t pos;
						while ((pos = inbuf.find('\n')) != std::string::npos)
						{
							std::string line = inbuf.substr(0, pos);
							inbuf.erase(0, pos + 1);
							if (!line.empty())
								on_line(line);
						}
					}
				}
			}
			handle_alerts();
			auto now = clk::now();
			check_dht(now);
			if (now - last_guard > std::chrono::seconds(60) || proxy_resolved())
			{
				update_guard(false);
				last_guard = now;
			}
			if (now - last_seed > std::chrono::seconds(1))
			{
				check_seeding();
				last_seed = now;
			}
			if (now - last_save > std::chrono::minutes(5))
			{
				for (auto &kv : handles)
					if (kv.second.is_valid() && !opts[kv.first].metadata_only && kv.second.need_save_resume_data())
						kv.second.save_resume_data(lt::torrent_handle::save_info_dict);
				save_session();
				last_save = now;
			}
		}
		dlog("shutting down");
		save_all(15000);
		if (client_fd >= 0)
			::close(client_fd);
		::close(listen_fd);
		::unlink(socket_path.c_str());
		ses->pause();
		ses.reset();
	}
};

static int check_resume(char const *fr, char const *tf)
{
	std::vector<char> rb, tb;
	if (!read_file(fr, rb) || !read_file(tf, tb))
	{
		printf("cannot read files\n");
		return 2;
	}
	lt::error_code ec;
	auto ti = std::make_shared<lt::torrent_info>(lt::span<char const>(tb.data(), long(tb.size())), ec, lt::from_span);
	if (ec)
	{
		printf("torrent: %s\n", ec.message().c_str());
		return 1;
	}
	lt::add_torrent_params p = lt::read_resume_data(rb, ec);
	if (ec)
	{
		printf("resume: %s\n", ec.message().c_str());
		return 1;
	}
	int have = 0;
	for (int i = 0; i < p.have_pieces.size(); ++i)
		if (p.have_pieces.get_bit(lt::piece_index_t(i)))
			++have;
	bool match = p.info_hashes.v1 == ti->info_hashes().v1 || p.info_hashes.v1.is_all_zeros();
	printf("usable=%d pieces=%d/%d have=%d save_path_set=%d\n", match ? 1 : 0, p.have_pieces.size(), ti->num_pieces(), have, p.save_path.empty() ? 0 : 1);
	return match ? 0 : 1;
}

int main(int argc, char **argv)
{
	Engine e;
	std::string settings_path;
	for (int i = 1; i < argc; ++i)
	{
		std::string a = argv[i];
		auto next = [&]() -> std::string { return i + 1 < argc ? argv[++i] : std::string(); };
		if (a == "--socket")
			e.socket_path = next();
		else if (a == "--state")
			e.state_dir = next();
		else if (a == "--torrents")
			e.torrents_dir = next();
		else if (a == "--settings")
			settings_path = next();
		else if (a == "--version")
		{
			printf("dc-bt %s libtorrent %s\n", DCBT_VERSION, LIBTORRENT_VERSION);
			return 0;
		}
		else if (a == "--check-resume" && i + 2 < argc)
			return check_resume(argv[i + 1], argv[i + 2]);
	}
	if (e.socket_path.empty() || e.state_dir.empty())
	{
		fprintf(stderr, "usage: dc-bt --socket <path> --state <dir> [--torrents <dir>] [--settings <file>]\n");
		return 2;
	}
	::mkdir(e.state_dir.c_str(), 0700);
	if (!e.torrents_dir.empty())
		::mkdir(e.torrents_dir.c_str(), 0700);
	signal(SIGTERM, on_signal);
	signal(SIGINT, on_signal);
	signal(SIGPIPE, SIG_IGN);
	umask(0002);
	json initial = json::object();
	std::vector<char> sb;
	if (!settings_path.empty() && read_file(settings_path, sb))
	{
		try
		{
			initial = json::parse(sb.begin(), sb.end());
		}
		catch (...)
		{
		}
	}
	try
	{
		e.start(initial);
	}
	catch (std::exception const &ex)
	{
		dlog("cannot start session: %s", ex.what());
		return 1;
	}
	if (!e.listen_socket())
	{
		dlog("cannot listen on %s: %s", e.socket_path.c_str(), strerror(errno));
		return 1;
	}
	dlog("ready (libtorrent %s), tuned for %s", LIBTORRENT_VERSION, hardware_json(e.hw, e.tune).dump().c_str());
	e.run();
	return 0;
}
