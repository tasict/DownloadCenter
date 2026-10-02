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

#include "json.hpp"

#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <poll.h>
#include <unistd.h>
#include <fcntl.h>
#include <dirent.h>
#include <signal.h>
#include <cerrno>
#include <cstdarg>
#include <cstdio>
#include <cstring>
#include <chrono>
#include <fstream>
#include <map>
#include <memory>
#include <set>
#include <sstream>
#include <string>
#include <vector>

namespace lt = libtorrent;
using json = nlohmann::json;
using clk = std::chrono::steady_clock;

static const char *DCBT_VERSION = "1.0";

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
	bool has_select = false;
	std::vector<int> select;
	std::map<int, int> priorities;
	std::string magnet_save; // metadata_only: where to write the .torrent
};

static json opt_json(Options const &o)
{
	json j;
	j["seed_ratio"] = o.seed_ratio;
	j["seed_time"] = o.seed_time;
	j["complete"] = o.complete;
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

// --- the engine ---

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
		p.set_str(lt::settings_pack::dht_bootstrap_nodes,
			"dht.libtorrent.org:25401,router.bittorrent.com:6881,router.utorrent.com:6881,dht.transmissionbt.com:6881,router.bitcomet.com:6881");
		// dcd does the queueing
		p.set_int(lt::settings_pack::active_downloads, -1);
		p.set_int(lt::settings_pack::active_seeds, -1);
		p.set_int(lt::settings_pack::active_limit, -1);
		p.set_int(lt::settings_pack::active_checking, 2);
		p.set_int(lt::settings_pack::alert_mask, lt::alert_category::status | lt::alert_category::error | lt::alert_category::storage);
		p.set_int(lt::settings_pack::alert_queue_size, 10000);
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
		ses.reset(new lt::session(std::move(sp)));
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
			if (o.complete)
				atp.flags |= lt::torrent_flags::paused;
			lt::torrent_handle h = ses->add_torrent(std::move(atp), ec);
			if (ec)
			{
				dlog("restore %s: %s", k.c_str(), ec.message().c_str());
				continue;
			}
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
		atp.flags &= ~lt::torrent_flags::auto_managed;
		atp.flags &= ~lt::torrent_flags::duplicate_is_error;
		if (req.value("paused", false))
			atp.flags |= lt::torrent_flags::paused;
		else
			atp.flags &= ~lt::torrent_flags::paused;
		if (req.value("sequential", false))
			atp.flags |= lt::torrent_flags::sequential_download;
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
		lt::torrent_handle h = ses->add_torrent(std::move(atp), ec);
		if (ec)
			throw std::runtime_error(ec.message());
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

	void forget(std::string const &k)
	{
		handles.erase(k);
		opts.erase(k);
		::unlink(resume_path(k).c_str());
		::unlink(opts_path(k).c_str());
	}

	json status_of(std::string const &k, lt::torrent_handle const &h, bool files)
	{
		lt::torrent_status s = h.status(lt::torrent_handle::query_pieces | lt::torrent_handle::query_name | lt::torrent_handle::query_save_path);
		Options &o = opts[k];
		json j;
		j["infohash"] = k;
		j["name"] = s.name;
		j["save_path"] = s.save_path;
		j["state"] = state_name(s.state);
		j["paused"] = bool(s.flags & lt::torrent_flags::paused);
		j["complete"] = o.complete;
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
		j["sequential"] = bool(s.flags & lt::torrent_flags::sequential_download);
		j["seeding_secs"] = (long long)(s.seeding_duration.count());
		j["finished_secs"] = (long long)(s.finished_duration.count());
		auto ti = h.torrent_file();
		if (ti)
		{
			j["pieces"] = bits_hex(s.pieces);
			j["num_pieces"] = ti->num_pieces();
			j["piece_length"] = ti->piece_length();
			j["comment"] = ti->comment();
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
			return json{{"dcbt", DCBT_VERSION}, {"libtorrent", LIBTORRENT_VERSION}};
		if (cmd == "apply_settings")
		{
			settings = req.value("settings", req);
			ses->apply_settings(pack_from(settings));
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
			h.force_reannounce();
			return json::object();
		}
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
				dlog("%s", ls->message().c_str());
			}
		}
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
		auto last_seed = clk::now(), last_save = clk::now();
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
	dlog("ready (libtorrent %s)", LIBTORRENT_VERSION);
	e.run();
	return 0;
}
