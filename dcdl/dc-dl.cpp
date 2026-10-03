// dc-dl: the libcurl transfer engine of Download Center.
//
// dcd connects to a unix socket once per transfer, sends one JSON line
// describing it (URL, byte range, headers, credentials, proxy) and reads
// frames back: H (response status and headers), D (data), E (result).
// Closing the connection aborts the transfer. All transfers run in one
// curl_multi event loop; when dcd reads slowly (its rate limit) the
// transfer is paused until the socket drains. See PROTOCOL.md.

#include <curl/curl.h>

#include "json.hpp"

#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <ifaddrs.h>
#include <netinet/in.h>
#include <signal.h>
#include <stdarg.h>
#include <stdio.h>
#include <string.h>
#include <sys/auxv.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <time.h>
#include <unistd.h>

#include <map>
#include <string>
#include <vector>

#if defined(__x86_64__) || defined(__i386__)
#include <cpuid.h>
#endif

using json = nlohmann::json;

static const char *VERSION = "1.1";
static const size_t MAX_PENDING = 256 * 1024; // buffered for dcd before the transfer pauses
static const size_t MAX_REQUEST = 1 << 20;
static const int MAX_CONNS = 256;
static const char *ALLOWED = "http,https,ftp,ftps,sftp,scp";

static volatile sig_atomic_t g_stop = 0;
static std::string g_knownHosts, g_caFile, g_caPath;
static bool g_aesHW = true; // AES-GCM runs on CPU instructions (see aesGcmInHardware)
static std::vector<std::string> g_ownAddrs; // the NAS's addresses (inet_ntop)
static time_t g_ownAt = 0;

static void dlog(const char *fmt, ...) __attribute__((format(printf, 1, 2)));
static void dlog(const char *fmt, ...)
{
	char ts[32];
	time_t now = time(nullptr);
	struct tm tm;
	localtime_r(&now, &tm);
	strftime(ts, sizeof ts, "%Y/%m/%d %H:%M:%S", &tm);
	fprintf(stderr, "%s ", ts);
	va_list ap;
	va_start(ap, fmt);
	vfprintf(stderr, fmt, ap);
	va_end(ap);
	fputc('\n', stderr);
	fflush(stderr);
}

// --- TLS cipher order -----------------------------------------------------

// aesGcmInHardware reports whether the CPU has AES and carry-less multiply
// instructions (AES-NI and PCLMULQDQ, or the ARMv8 AES and PMULL
// extensions), which OpenSSL uses for AES-GCM. Without them (the 32-bit ARM
// models, some ARMv8 chips, older Atoms) ChaCha20-Poly1305 is several times
// faster, and a single dc-dl thread does all TLS decryption.
static bool aesGcmInHardware()
{
#if defined(__x86_64__) || defined(__i386__)
	unsigned a, b, c, d;
	if (!__get_cpuid(1, &a, &b, &c, &d))
		return true;
	return (c & bit_AES) && (c & bit_PCLMUL);
#elif defined(__aarch64__)
	unsigned long hw = getauxval(AT_HWCAP);
	return (hw & (1UL << 3)) && (hw & (1UL << 4)); // HWCAP_AES, HWCAP_PMULL
#elif defined(__arm__)
	unsigned long hw2 = getauxval(AT_HWCAP2);
	return (hw2 & (1UL << 0)) && (hw2 & (1UL << 1)); // HWCAP2_AES, HWCAP2_PMULL
#else
	return true;
#endif
}

// Without AES hardware ChaCha20-Poly1305 is offered first; the set of
// ciphers stays OpenSSL's default, only the order changes. Servers that
// follow the client's preference (most do for TLS 1.3) then pick it.
static const char *CHACHA_FIRST_TLS13 = "TLS_CHACHA20_POLY1305_SHA256:TLS_AES_128_GCM_SHA256:TLS_AES_256_GCM_SHA384";
static const char *CHACHA_FIRST_TLS12 = "DEFAULT:+AES:+CAMELLIA:+ARIA";

// --- address guard -------------------------------------------------------

static void refreshOwnAddrs()
{
	time_t now = time(nullptr);
	if (now - g_ownAt < 60)
		return;
	g_ownAt = now;
	g_ownAddrs.clear();
	struct ifaddrs *ifa = nullptr;
	if (getifaddrs(&ifa) != 0)
		return;
	char buf[INET6_ADDRSTRLEN];
	for (struct ifaddrs *i = ifa; i; i = i->ifa_next)
	{
		if (!i->ifa_addr)
			continue;
		if (i->ifa_addr->sa_family == AF_INET)
			inet_ntop(AF_INET, &((struct sockaddr_in *)i->ifa_addr)->sin_addr, buf, sizeof buf);
		else if (i->ifa_addr->sa_family == AF_INET6)
			inet_ntop(AF_INET6, &((struct sockaddr_in6 *)i->ifa_addr)->sin6_addr, buf, sizeof buf);
		else
			continue;
		g_ownAddrs.push_back(buf);
	}
	freeifaddrs(ifa);
}

static bool forbiddenV4(const unsigned char *b)
{
	return b[0] == 127 || b[0] == 0 || (b[0] == 169 && b[1] == 254) || b[0] >= 224;
}

// forbidden matches netutil.Forbidden in dcd: loopback, link-local,
// unspecified, multicast and the NAS's own addresses.
static bool forbidden(const struct sockaddr *sa)
{
	char buf[INET6_ADDRSTRLEN] = "";
	if (sa->sa_family == AF_INET)
	{
		const struct sockaddr_in *s4 = (const struct sockaddr_in *)sa;
		if (forbiddenV4((const unsigned char *)&s4->sin_addr))
			return true;
		inet_ntop(AF_INET, &s4->sin_addr, buf, sizeof buf);
	}
	else if (sa->sa_family == AF_INET6)
	{
		const struct sockaddr_in6 *s6 = (const struct sockaddr_in6 *)sa;
		const unsigned char *b = s6->sin6_addr.s6_addr;
		if (IN6_IS_ADDR_LOOPBACK(&s6->sin6_addr) || IN6_IS_ADDR_UNSPECIFIED(&s6->sin6_addr) ||
			IN6_IS_ADDR_LINKLOCAL(&s6->sin6_addr) || IN6_IS_ADDR_MULTICAST(&s6->sin6_addr))
			return true;
		if (IN6_IS_ADDR_V4MAPPED(&s6->sin6_addr))
		{
			if (forbiddenV4(b + 12))
				return true;
			inet_ntop(AF_INET, b + 12, buf, sizeof buf);
		}
		else
			inet_ntop(AF_INET6, &s6->sin6_addr, buf, sizeof buf);
	}
	else
		return true;
	refreshOwnAddrs();
	for (const std::string &a : g_ownAddrs)
		if (a == buf)
			return true;
	return false;
}

// --- connections -----------------------------------------------------------

struct Conn
{
	int fd = -1;
	std::string in;
	bool started = false;
	CURL *easy = nullptr;
	struct curl_slist *hdrs = nullptr;
	std::string out;
	size_t outOff = 0;
	bool paused = false;
	bool headerSent = false;
	bool finished = false; // E frame queued: close once flushed
	bool guard = false;
	bool proxied = false;
	bool blocked = false;
	bool hostKeyChanged = false;
	std::vector<std::string> headers;
	char err[CURL_ERROR_SIZE] = "";
	std::string url;
};

static std::map<int, Conn *> g_conns;
static CURLM *g_multi = nullptr;

static size_t pending(Conn *c) { return c->out.size() - c->outOff; }

static void frame(Conn *c, char type, const char *p, size_t n)
{
	unsigned char h[5] = {(unsigned char)type, (unsigned char)(n >> 24), (unsigned char)(n >> 16), (unsigned char)(n >> 8), (unsigned char)n};
	c->out.append((const char *)h, 5);
	c->out.append(p, n);
}

static void frameJSON(Conn *c, char type, const json &j)
{
	std::string s = j.dump(-1, ' ', false, json::error_handler_t::replace);
	frame(c, type, s.data(), s.size());
}

// flush sends what the socket takes without blocking; false on a dead peer.
static bool flush(Conn *c)
{
	while (pending(c) > 0)
	{
		ssize_t n = send(c->fd, c->out.data() + c->outOff, pending(c), MSG_NOSIGNAL | MSG_DONTWAIT);
		if (n > 0)
		{
			c->outOff += n;
			continue;
		}
		if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK))
			break;
		if (n < 0 && errno == EINTR)
			continue;
		return false;
	}
	if (c->outOff > 0 && c->outOff == c->out.size())
	{
		c->out.clear();
		c->outOff = 0;
	}
	else if (c->outOff > (1 << 20))
	{
		c->out.erase(0, c->outOff);
		c->outOff = 0;
	}
	return true;
}

static void sendHeader(Conn *c)
{
	if (c->headerSent)
		return;
	c->headerSent = true;
	long status = 0;
	curl_off_t length = -1, filetime = -1;
	char *eff = nullptr;
	if (c->easy)
	{
		curl_easy_getinfo(c->easy, CURLINFO_RESPONSE_CODE, &status);
		curl_easy_getinfo(c->easy, CURLINFO_CONTENT_LENGTH_DOWNLOAD_T, &length);
		curl_easy_getinfo(c->easy, CURLINFO_FILETIME_T, &filetime);
		curl_easy_getinfo(c->easy, CURLINFO_EFFECTIVE_URL, &eff);
	}
	frameJSON(c, 'H', {{"status", status}, {"headers", c->headers}, {"url", eff ? eff : c->url}, {"length", (long long)length}, {"filetime", (long long)filetime}});
}

static void closeConn(Conn *c)
{
	if (c->easy)
	{
		curl_multi_remove_handle(g_multi, c->easy);
		curl_easy_cleanup(c->easy);
		c->easy = nullptr;
	}
	if (c->hdrs)
		curl_slist_free_all(c->hdrs);
	close(c->fd);
	g_conns.erase(c->fd);
	delete c;
}

// --- libcurl callbacks -----------------------------------------------------

static size_t onWrite(char *p, size_t size, size_t n, void *ud)
{
	Conn *c = (Conn *)ud;
	size_t len = size * n;
	if (pending(c) >= MAX_PENDING)
	{
		// dcd is not reading (rate limit or busy): wait for the socket
		c->paused = true;
		return CURL_WRITEFUNC_PAUSE;
	}
	sendHeader(c);
	frame(c, 'D', p, len);
	flush(c);
	return len;
}

static size_t onHeader(char *p, size_t size, size_t n, void *ud)
{
	Conn *c = (Conn *)ud;
	size_t len = size * n;
	std::string line(p, len);
	while (!line.empty() && (line.back() == '\r' || line.back() == '\n'))
		line.pop_back();
	// A new response (redirect, 100-continue): keep only the last one
	if (line.compare(0, 5, "HTTP/") == 0)
		c->headers.clear();
	if (!line.empty() && c->headers.size() < 200)
		c->headers.push_back(line);
	return len;
}

static curl_socket_t onOpenSocket(void *ud, curlsocktype purpose, struct curl_sockaddr *a)
{
	Conn *c = (Conn *)ud;
	(void)purpose;
	if (c->guard && !c->proxied && forbidden(&a->addr))
	{
		c->blocked = true;
		return CURL_SOCKET_BAD;
	}
	return socket(a->family, a->socktype, a->protocol);
}

static int onHostKey(CURL *, const struct curl_khkey *, const struct curl_khkey *, enum curl_khmatch m, void *ud)
{
	Conn *c = (Conn *)ud;
	switch (m)
	{
	case CURLKHMATCH_OK:
		return CURLKHSTAT_FINE;
	case CURLKHMATCH_MISSING:
		// Trust on first use: remember the key
		return CURLKHSTAT_FINE_ADD_TO_FILE;
	default:
		c->hostKeyChanged = true;
		return CURLKHSTAT_REJECT;
	}
}

// --- requests --------------------------------------------------------------

static std::string schemeOf(const std::string &url)
{
	size_t p = url.find("://");
	std::string s = p == std::string::npos ? "" : url.substr(0, p);
	for (char &ch : s)
		ch = (char)tolower((unsigned char)ch);
	return s;
}

static std::string str(const json &j, const char *k)
{
	auto it = j.find(k);
	return it != j.end() && it->is_string() ? it->get<std::string>() : std::string();
}

static void versionReply(Conn *c)
{
	curl_version_info_data *v = curl_version_info(CURLVERSION_NOW);
	json protos = json::array();
	for (const char *const *p = v->protocols; *p; p++)
		protos.push_back(*p);
	json j = {{"dcdl", VERSION}, {"curl", v->version}, {"ssl", v->ssl_version ? v->ssl_version : ""},
			  {"libssh", v->libssh_version ? v->libssh_version : ""}, {"nghttp2", v->nghttp2_version ? v->nghttp2_version : ""},
			  {"http2", (v->features & CURL_VERSION_HTTP2) != 0}, {"aes_hw", g_aesHW}, {"protocols", protos}};
	std::string s = j.dump() + "\n";
	c->out.append(s);
	c->finished = true;
	flush(c);
}

static void fail(Conn *c, const std::string &msg)
{
	sendHeader(c);
	frameJSON(c, 'E', {{"code", (int)CURLE_BAD_FUNCTION_ARGUMENT}, {"error", msg}, {"blocked", false}, {"hostkey_changed", false}});
	c->finished = true;
	flush(c);
}

static void start(Conn *c, const std::string &line)
{
	c->started = true;
	json r;
	try
	{
		r = json::parse(line);
	}
	catch (...)
	{
		fail(c, "bad request");
		return;
	}
	if (str(r, "cmd") == "version")
	{
		versionReply(c);
		return;
	}
	c->url = str(r, "url");
	std::string scheme = schemeOf(c->url);
	std::string allowed = std::string(",") + ALLOWED + ",";
	if (scheme.empty() || allowed.find("," + scheme + ",") == std::string::npos)
	{
		fail(c, "unsupported protocol");
		return;
	}
	CURL *e = curl_easy_init();
	if (!e)
	{
		fail(c, "out of memory");
		return;
	}
	c->easy = e;
	c->guard = r.value("guard", false);
	std::string proxy = str(r, "proxy");
	c->proxied = !proxy.empty();
	bool http = scheme == "http" || scheme == "https";

	curl_easy_setopt(e, CURLOPT_URL, c->url.c_str());
	curl_easy_setopt(e, CURLOPT_PRIVATE, c);
	curl_easy_setopt(e, CURLOPT_ERRORBUFFER, c->err);
	curl_easy_setopt(e, CURLOPT_NOSIGNAL, 1L);
	curl_easy_setopt(e, CURLOPT_PROTOCOLS_STR, ALLOWED);
	curl_easy_setopt(e, CURLOPT_REDIR_PROTOCOLS_STR, "http,https");
	curl_easy_setopt(e, CURLOPT_WRITEFUNCTION, onWrite);
	curl_easy_setopt(e, CURLOPT_WRITEDATA, c);
	curl_easy_setopt(e, CURLOPT_HEADERFUNCTION, onHeader);
	curl_easy_setopt(e, CURLOPT_HEADERDATA, c);
	curl_easy_setopt(e, CURLOPT_OPENSOCKETFUNCTION, onOpenSocket);
	curl_easy_setopt(e, CURLOPT_OPENSOCKETDATA, c);
	curl_easy_setopt(e, CURLOPT_CONNECTTIMEOUT, 30L);
	curl_easy_setopt(e, CURLOPT_TCP_KEEPALIVE, 1L);
	curl_easy_setopt(e, CURLOPT_BUFFERSIZE, 65536L);
	curl_easy_setopt(e, CURLOPT_FILETIME, 1L);
	if (!g_aesHW)
	{
		curl_easy_setopt(e, CURLOPT_TLS13_CIPHERS, CHACHA_FIRST_TLS13);
		curl_easy_setopt(e, CURLOPT_SSL_CIPHER_LIST, CHACHA_FIRST_TLS12);
		curl_easy_setopt(e, CURLOPT_PROXY_TLS13_CIPHERS, CHACHA_FIRST_TLS13);
		curl_easy_setopt(e, CURLOPT_PROXY_SSL_CIPHER_LIST, CHACHA_FIRST_TLS12);
	}
	// Never pick up proxies from the environment
	curl_easy_setopt(e, CURLOPT_PROXY, proxy.c_str());
	curl_easy_setopt(e, CURLOPT_NOPROXY, "");
	if (!g_caFile.empty())
		curl_easy_setopt(e, CURLOPT_CAINFO, g_caFile.c_str());
	if (!g_caPath.empty())
		curl_easy_setopt(e, CURLOPT_CAPATH, g_caPath.c_str());
	std::string ua = str(r, "ua");
	curl_easy_setopt(e, CURLOPT_USERAGENT, ua.empty() ? "DownloadCenter/1.0" : ua.c_str());
	if (r.value("head", false))
		curl_easy_setopt(e, CURLOPT_NOBODY, 1L);
	std::string range = str(r, "range");
	if (!range.empty())
		curl_easy_setopt(e, CURLOPT_RANGE, range.c_str());
	std::string user = str(r, "user");
	if (!user.empty())
	{
		curl_easy_setopt(e, CURLOPT_USERNAME, user.c_str());
		curl_easy_setopt(e, CURLOPT_PASSWORD, str(r, "pass").c_str());
	}
	if (http)
	{
		curl_easy_setopt(e, CURLOPT_FOLLOWLOCATION, 1L);
		curl_easy_setopt(e, CURLOPT_MAXREDIRS, 10L);
		// Credentials go out at once (no 401 round trip per range) and are
		// dropped on redirects to another host
		curl_easy_setopt(e, CURLOPT_HTTPAUTH, (long)CURLAUTH_BASIC);
		auto hs = r.find("headers");
		if (hs != r.end() && hs->is_array())
			for (const auto &h : *hs)
				if (h.is_string())
				{
					std::string v = h.get<std::string>();
					if (v.find_first_of("\r\n") == std::string::npos)
						c->hdrs = curl_slist_append(c->hdrs, v.c_str());
				}
		if (c->hdrs)
			curl_easy_setopt(e, CURLOPT_HTTPHEADER, c->hdrs);
	}
	if (scheme == "ftp" || scheme == "ftps")
		curl_easy_setopt(e, CURLOPT_FTP_SKIP_PASV_IP, 1L);
	if (scheme == "sftp" || scheme == "scp")
	{
		curl_easy_setopt(e, CURLOPT_SSH_AUTH_TYPES, (long)(CURLSSH_AUTH_PASSWORD | CURLSSH_AUTH_KEYBOARD));
		curl_easy_setopt(e, CURLOPT_SSH_KNOWNHOSTS, g_knownHosts.c_str());
		curl_easy_setopt(e, CURLOPT_SSH_KEYFUNCTION, onHostKey);
		curl_easy_setopt(e, CURLOPT_SSH_KEYDATA, c);
	}
	if (curl_multi_add_handle(g_multi, e) != CURLM_OK)
	{
		curl_easy_cleanup(e);
		c->easy = nullptr;
		fail(c, "cannot start the transfer");
	}
}

static void finish(Conn *c, CURLcode res)
{
	sendHeader(c);
	std::string msg = c->err[0] ? c->err : curl_easy_strerror(res);
	if (c->blocked)
		msg = "address is not allowed";
	if (c->hostKeyChanged)
		msg = "the server's host key changed";
	frameJSON(c, 'E', {{"code", (int)res}, {"error", res == CURLE_OK ? "" : msg}, {"blocked", c->blocked}, {"hostkey_changed", c->hostKeyChanged}});
	curl_multi_remove_handle(g_multi, c->easy);
	curl_easy_cleanup(c->easy);
	c->easy = nullptr;
	c->finished = true;
	flush(c);
}

// --- main loop -------------------------------------------------------------

static void onSignal(int) { g_stop = 1; }

static int listenOn(const std::string &path)
{
	int fd = socket(AF_UNIX, SOCK_STREAM, 0);
	if (fd < 0)
		return -1;
	struct sockaddr_un sa;
	memset(&sa, 0, sizeof sa);
	sa.sun_family = AF_UNIX;
	if (path.size() >= sizeof sa.sun_path)
		return -1;
	strcpy(sa.sun_path, path.c_str());
	unlink(path.c_str());
	mode_t old = umask(0077);
	int rc = bind(fd, (struct sockaddr *)&sa, sizeof sa);
	umask(old);
	if (rc != 0 || listen(fd, 64) != 0)
		return -1;
	chmod(path.c_str(), 0600);
	fcntl(fd, F_SETFL, fcntl(fd, F_GETFL) | O_NONBLOCK);
	return fd;
}

static bool exists(const std::string &p)
{
	struct stat st;
	return stat(p.c_str(), &st) == 0;
}

int main(int argc, char **argv)
{
	std::string sock;
	for (int i = 1; i < argc; i++)
	{
		std::string a = argv[i];
		if (a == "--socket" && i + 1 < argc)
			sock = argv[++i];
		else if (a == "--known-hosts" && i + 1 < argc)
			g_knownHosts = argv[++i];
		else if (a == "--prefer-chacha")
			g_aesHW = false;
		else if (a == "--version")
		{
			printf("dc-dl %s, %s\n", VERSION, curl_version());
			return 0;
		}
	}
	if (sock.empty() || g_knownHosts.empty())
	{
		fprintf(stderr, "usage: dc-dl --socket <path> --known-hosts <file>\n");
		return 2;
	}
	signal(SIGPIPE, SIG_IGN);
	signal(SIGTERM, onSignal);
	signal(SIGINT, onSignal);
	if (!exists(g_knownHosts))
	{
		int k = open(g_knownHosts.c_str(), O_CREAT | O_WRONLY, 0600);
		if (k >= 0)
			close(k);
	}
	for (const char *p : {"/etc/ssl/certs/ca-certificates.crt", "/etc/ssl/ca-bundle.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem"})
		if (exists(p))
		{
			g_caFile = p;
			break;
		}
	if (exists("/etc/ssl/certs"))
		g_caPath = "/etc/ssl/certs";
	if (g_aesHW)
		g_aesHW = aesGcmInHardware();
	if (curl_global_init(CURL_GLOBAL_ALL) != CURLE_OK)
		return 1;
	g_multi = curl_multi_init();
	// dcd splits a download into ranges to get one TCP connection each.
	// With multiplexing (libcurl's default) every range after the first
	// joins the first range's HTTP/2 connection, and one TCP connection is
	// much slower on long or lossy routes. Idle connections are still reused.
	curl_multi_setopt(g_multi, CURLMOPT_PIPELINING, (long)CURLPIPE_NOTHING);
	int lfd = listenOn(sock);
	if (lfd < 0)
	{
		dlog("cannot listen on %s: %s", sock.c_str(), strerror(errno));
		return 1;
	}
	dlog("dc-dl %s started (%s), CA %s, %s", VERSION, curl_version(), g_caFile.empty() ? g_caPath.c_str() : g_caFile.c_str(),
		 g_aesHW ? "AES-GCM in hardware" : "no AES hardware, ChaCha20 first");

	std::vector<struct curl_waitfd> wfds;
	std::vector<Conn *> order;
	while (!g_stop)
	{
		wfds.clear();
		order.clear();
		wfds.push_back({lfd, CURL_WAIT_POLLIN, 0});
		for (auto &kv : g_conns)
		{
			Conn *c = kv.second;
			short ev = CURL_WAIT_POLLIN;
			if (pending(c) > 0)
				ev |= CURL_WAIT_POLLOUT;
			wfds.push_back({c->fd, ev, 0});
			order.push_back(c);
		}
		int n = 0;
		curl_multi_poll(g_multi, wfds.data(), (unsigned)wfds.size(), 1000, &n);

		if (wfds[0].revents & CURL_WAIT_POLLIN)
			for (;;)
			{
				int fd = accept(lfd, nullptr, nullptr);
				if (fd < 0)
					break;
				if ((int)g_conns.size() >= MAX_CONNS)
				{
					close(fd);
					continue;
				}
				fcntl(fd, F_SETFL, fcntl(fd, F_GETFL) | O_NONBLOCK);
				Conn *c = new Conn;
				c->fd = fd;
				g_conns[fd] = c;
			}

		for (size_t i = 0; i < order.size(); i++)
		{
			Conn *c = order[i];
			short re = wfds[i + 1].revents;
			bool dead = false;
			if (re & CURL_WAIT_POLLIN)
			{
				char buf[4096];
				for (;;)
				{
					ssize_t r = recv(c->fd, buf, sizeof buf, 0);
					if (r > 0)
					{
						if (!c->started)
						{
							c->in.append(buf, r);
							size_t nl = c->in.find('\n');
							if (nl != std::string::npos)
							{
								std::string line = c->in.substr(0, nl);
								c->in.clear();
								start(c, line);
							}
							else if (c->in.size() > MAX_REQUEST)
							{
								dead = true;
								break;
							}
						}
						continue; // anything after the request is ignored
					}
					if (r == 0 || (errno != EAGAIN && errno != EWOULDBLOCK && errno != EINTR))
						dead = true; // dcd closed the connection: abort
					break;
				}
			}
			if (!dead && !flush(c))
				dead = true;
			if (dead || (c->finished && pending(c) == 0))
			{
				closeConn(c);
				continue;
			}
			if (c->paused && pending(c) < MAX_PENDING / 2 && c->easy)
			{
				c->paused = false;
				curl_easy_pause(c->easy, CURLPAUSE_CONT);
			}
		}

		int running = 0;
		curl_multi_perform(g_multi, &running);
		CURLMsg *m;
		int left;
		while ((m = curl_multi_info_read(g_multi, &left)))
		{
			if (m->msg != CURLMSG_DONE)
				continue;
			Conn *c = nullptr;
			curl_easy_getinfo(m->easy_handle, CURLINFO_PRIVATE, (char **)&c);
			if (c)
			{
				finish(c, m->data.result);
				if (pending(c) == 0)
					closeConn(c);
			}
		}
	}
	dlog("dc-dl stopping");
	while (!g_conns.empty())
		closeConn(g_conns.begin()->second);
	curl_multi_cleanup(g_multi);
	curl_global_cleanup();
	close(lfd);
	unlink(sock.c_str());
	return 0;
}
