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
#include <libtorrent/hasher.hpp>
#include <openssl/x509.h>

#include "json.hpp"

#include <sys/ioctl.h>
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
#include <pthread.h>
#include <atomic>
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

#ifndef FS_IOC_GETVERSION
#define FS_IOC_GETVERSION _IOR('v', 1, long) // as <linux/fs.h>: the inode's generation
#endif

namespace lt = libtorrent;
using json = nlohmann::json;
using clk = std::chrono::steady_clock;

static const char *DCBT_VERSION = "1.8";

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
	int fd = ::open(tmp.c_str(), O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0600);
	if (fd < 0)
		return false;
	::fchmod(fd, 0600); // also when an old one was left behind
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

// --- folders a regular user can change ---
//
// dc-bt runs as root. A regular user's torrent (untrusted_dir) is downloaded
// and seeded only in a folder only root can enter; its data reaches the
// user's folder as hard links made through folder descriptors, never by
// path, so no link the user put there is followed (see PROTOCOL.md).

struct Fd
{
	int fd = -1;
	Fd() = default;
	explicit Fd(int f) : fd(f) {}
	Fd(Fd const &) = delete;
	Fd &operator=(Fd const &) = delete;
	Fd(Fd &&o) noexcept : fd(o.fd) { o.fd = -1; }
	Fd &operator=(Fd &&o) noexcept
	{
		if (this != &o)
		{
			reset();
			fd = o.fd;
			o.fd = -1;
		}
		return *this;
	}
	~Fd() { reset(); }
	void reset()
	{
		if (fd >= 0)
			::close(fd);
		fd = -1;
	}
	bool ok() const { return fd >= 0; }
};

static std::vector<std::string> split_path(std::string const &p)
{
	std::vector<std::string> out;
	size_t i = 0;
	while (i < p.size())
	{
		size_t j = p.find('/', i);
		if (j == std::string::npos)
			j = p.size();
		if (j > i)
			out.push_back(p.substr(i, j - i));
		i = j + 1;
	}
	return out;
}

static bool plain_name(std::string const &n)
{
	return !n.empty() && n != "." && n != ".." && n.find('/') == std::string::npos;
}

// open_dir opens an absolute folder without following a link anyone but
// root could have made: a link is followed only when root owns it and the
// folder it sits in is root's and writable by nobody else, or sticky (the
// links QTS keeps itself, like /share/homes). Every step is opened with
// O_NOFOLLOW from the folder before it, so nothing changes under the walk.
static Fd open_dir(std::string const &path, std::string &err)
{
	if (path.empty() || path[0] != '/')
	{
		err = "not an absolute path: " + path;
		return Fd();
	}
	std::vector<std::string> todo = split_path(path);
	for (auto const &c : todo)
		if (c == "..")
		{
			err = "not a plain path: " + path;
			return Fd();
		}
	Fd cur(::open("/", O_RDONLY | O_DIRECTORY | O_CLOEXEC));
	int links = 0;
	size_t at = 0;
	while (cur.ok() && at < todo.size())
	{
		std::string c = todo[at++];
		if (c.empty() || c == ".")
			continue;
		struct stat st;
		if (::fstatat(cur.fd, c.c_str(), &st, AT_SYMLINK_NOFOLLOW) != 0)
		{
			err = path + ": " + strerror(errno);
			return Fd();
		}
		if (S_ISLNK(st.st_mode))
		{
			struct stat ds;
			bool trusted = ::fstat(cur.fd, &ds) == 0 && st.st_uid == 0 && ds.st_uid == 0 &&
				(!(ds.st_mode & (S_IWGRP | S_IWOTH)) || (ds.st_mode & S_ISVTX));
			if (!trusted || ++links > 16)
			{
				err = path + ": leads through a link (" + c + ")";
				return Fd();
			}
			char buf[4096];
			ssize_t n = ::readlinkat(cur.fd, c.c_str(), buf, sizeof buf - 1);
			if (n <= 0)
			{
				err = path + ": " + strerror(errno);
				return Fd();
			}
			std::string target(buf, size_t(n));
			std::vector<std::string> rest(todo.begin() + long(at), todo.end());
			todo = split_path(target);
			todo.insert(todo.end(), rest.begin(), rest.end());
			at = 0;
			if (target[0] == '/')
				cur = Fd(::open("/", O_RDONLY | O_DIRECTORY | O_CLOEXEC));
			continue;
		}
		Fd next(::openat(cur.fd, c.c_str(), O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC));
		if (!next.ok())
		{
			err = path + ": " + strerror(errno);
			return Fd();
		}
		cur = std::move(next);
	}
	if (!cur.ok())
		err = path + ": " + strerror(errno);
	return cur;
}

// not_shared: root owns the folder and nobody else may write in it.
static bool not_shared(struct stat const &st)
{
	return S_ISDIR(st.st_mode) && st.st_uid == 0 && !(st.st_mode & (S_IWGRP | S_IWOTH));
}

// private_dir: a folder only root can change, in a folder only root can
// change, so its path keeps leading to it while libtorrent uses the path.
static bool private_dir(std::string const &path, std::string &err)
{
	Fd d = open_dir(path, err);
	if (!d.ok())
		return false;
	struct stat st, pst;
	Fd parent(::openat(d.fd, "..", O_RDONLY | O_DIRECTORY | O_CLOEXEC));
	if (::fstat(d.fd, &st) != 0 || !parent.ok() || ::fstat(parent.fd, &pst) != 0)
	{
		err = path + ": " + strerror(errno);
		return false;
	}
	if (!not_shared(st) || !not_shared(pst))
	{
		err = path + ": not a folder only root can change";
		return false;
	}
	return true;
}

static std::string dir_name(std::string const &p)
{
	size_t i = p.find_last_of('/');
	return i == std::string::npos || i == 0 ? std::string("/") : p.substr(0, i);
}

static std::string base_name(std::string const &p)
{
	size_t i = p.find_last_of('/');
	return i == std::string::npos ? p : p.substr(i + 1);
}

// remove_tree deletes name in dir and everything below it, without
// following links.
static void remove_tree(int dir, std::string const &name, int depth = 0)
{
	if (depth > 64)
		return;
	if (::unlinkat(dir, name.c_str(), 0) == 0 || errno == ENOENT)
		return;
	Fd d(::openat(dir, name.c_str(), O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC));
	if (!d.ok())
		return;
	int dup = ::dup(d.fd);
	DIR *dp = dup >= 0 ? ::fdopendir(dup) : nullptr;
	if (!dp)
	{
		if (dup >= 0)
			::close(dup);
		return;
	}
	std::vector<std::string> names;
	while (struct dirent *e = ::readdir(dp))
	{
		std::string n = e->d_name;
		if (n != "." && n != "..")
			names.push_back(n);
	}
	::closedir(dp);
	for (auto const &n : names)
		remove_tree(d.fd, n, depth + 1);
	::unlinkat(dir, name.c_str(), AT_REMOVEDIR);
}

// One file to hand over: its path below the private folder and below the
// destination, or no source for an empty file libtorrent never wrote.
struct HandFile
{
	std::vector<std::string> src, dst;
	bool empty = false;
};

// hand_over gives the files to the user's folder dst (an absolute path) as
// hard links of the files in the private folder src. The top folder (or the
// single file) is created new; with merge it may exist (files chosen later)
// and what is there already is left alone. Folders are made through
// descriptors with O_NOFOLLOW and stay root's until they are complete; then
// they, and the linked files, belong to the owner of dst. Returns "" or why
// it failed.
static std::string hand_over(std::string const &src, std::string const &dst, std::vector<HandFile> const &files,
	bool merge, int owner_uid, int owner_gid, std::vector<std::string> &skipped)
{
	std::string err;
	Fd s = open_dir(src, err);
	if (!s.ok())
		return err;
	Fd d = open_dir(dst, err);
	if (!d.ok())
		return err;
	struct stat sst, dst_st;
	if (::fstat(s.fd, &sst) != 0 || ::fstat(d.fd, &dst_st) != 0)
		return strerror(errno);
	if (sst.st_dev != dst_st.st_dev)
		return "the temporary folder is on another volume than " + dst;
	uid_t uid = owner_uid > 0 ? uid_t(owner_uid) : dst_st.st_uid;
	gid_t gid = owner_uid > 0 && owner_gid >= 0 ? gid_t(owner_gid) : dst_st.st_gid;

	// The open chain of destination folders, by path below dst. A folder
	// made here stays root's (0700) while files go in and is given to the
	// owner of dst when the walk leaves it
	struct Step
	{
		std::string name;
		Fd fd;
		bool made;
	};
	std::vector<Step> chain;
	auto leave = [&](size_t keep) {
		while (chain.size() > keep)
		{
			if (chain.back().made)
			{
				::fchmod(chain.back().fd.fd, 0775);
				::fchown(chain.back().fd.fd, uid, gid);
			}
			chain.pop_back();
		}
	};
	std::string result;
	// What the hand-over made at the top of dst (folder or single file)
	bool top_made = false;
	std::string top_name;
	struct stat top_st;
	auto made_top = [&](std::string const &name, struct stat const &st) {
		if (!top_made)
		{
			top_made = true;
			top_name = name;
			top_st = st;
		}
	};
	for (auto const &f : files)
	{
		if (f.dst.empty())
			continue;
		bool bad = false;
		for (auto const &c : f.dst)
			bad = bad || !plain_name(c);
		for (auto const &c : f.src)
			bad = bad || !plain_name(c);
		if (bad)
		{
			skipped.push_back("bad name");
			continue;
		}
		// Destination folder of this file, opened step by step
		size_t keep = 0;
		while (keep < chain.size() && keep + 1 < f.dst.size() && chain[keep].name == f.dst[keep])
			++keep;
		leave(keep);
		int at = chain.empty() ? d.fd : chain.back().fd.fd;
		bool ok = true;
		for (size_t i = keep; i + 1 < f.dst.size(); ++i)
		{
			std::string const &c = f.dst[i];
			bool fresh = false;
			if (::mkdirat(at, c.c_str(), 0700) == 0)
				fresh = true;
			else if (errno != EEXIST || (i == 0 && !merge))
			{
				result = (errno == EEXIST ? "File exists: " : std::string(strerror(errno)) + ": ") + dst + "/" + c;
				ok = false;
				break;
			}
			Fd nd(::openat(at, c.c_str(), O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC));
			struct stat ns;
			if (!nd.ok() || ::fstat(nd.fd, &ns) != 0 || (fresh && ns.st_uid != 0))
			{
				if (!merge)
				{
					result = "the destination changed while the files were handed over: " + dst + "/" + c;
					ok = false;
					break;
				}
				skipped.push_back(c);
				ok = false;
				break;
			}
			if (i == 0 && fresh)
				made_top(c, ns);
			chain.push_back(Step{c, std::move(nd), fresh});
			at = chain.back().fd.fd;
		}
		if (!ok)
		{
			if (!merge)
				break;
			continue;
		}
		std::string const &leaf = f.dst.back();
		if (f.empty)
		{
			Fd nf(::openat(at, leaf.c_str(), O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0664));
			struct stat es;
			if (nf.ok())
			{
				::fchown(nf.fd, uid, gid);
				if (f.dst.size() == 1 && ::fstat(nf.fd, &es) == 0)
					made_top(leaf, es);
			}
			else if (errno != EEXIST || !merge)
			{
				result = std::string(strerror(errno)) + ": " + dst + "/" + leaf;
				break;
			}
			continue;
		}
		// The source: a regular file in the private folder
		Fd sd(::dup(s.fd));
		for (size_t i = 0; sd.ok() && i + 1 < f.src.size(); ++i)
			sd = Fd(::openat(sd.fd, f.src[i].c_str(), O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC));
		struct stat fs;
		if (!sd.ok() || ::fstatat(sd.fd, f.src.back().c_str(), &fs, AT_SYMLINK_NOFOLLOW) != 0 || !S_ISREG(fs.st_mode))
		{
			if (!merge)
			{
				result = "missing in the temporary folder: " + f.src.back();
				break;
			}
			skipped.push_back(leaf);
			continue;
		}
		if (::linkat(sd.fd, f.src.back().c_str(), at, leaf.c_str(), 0) != 0)
		{
			struct stat es;
			bool same = errno == EEXIST && ::fstatat(at, leaf.c_str(), &es, AT_SYMLINK_NOFOLLOW) == 0 &&
				es.st_dev == fs.st_dev && es.st_ino == fs.st_ino;
			if (!same)
			{
				if (merge)
				{
					skipped.push_back(leaf);
					continue;
				}
				result = (errno == EEXIST ? "File exists: " : std::string(strerror(errno)) + ": ") + dst + "/" + leaf;
				break;
			}
		}
		if (f.dst.size() == 1)
			made_top(leaf, fs);
		::fchownat(sd.fd, f.src.back().c_str(), uid, gid, AT_SYMLINK_NOFOLLOW);
	}
	// The name the data was handed over under must still be it: renamed
	// meanwhile (and something else put in its place), the data is not where
	// dcd will look for it
	if (result.empty() && !merge && top_made)
	{
		struct stat now;
		if (::fstatat(d.fd, top_name.c_str(), &now, AT_SYMLINK_NOFOLLOW) != 0 || now.st_dev != top_st.st_dev || now.st_ino != top_st.st_ino)
			result = "the destination changed while the files were handed over: " + dst + "/" + top_name;
	}
	leave(0);
	return result;
}

// --- data already in the destination ---
//
// A torrent whose data already sits in the user's folder (seeded from there
// before 1.5, removed and added again, or copied there) takes it over
// instead of downloading it anew. Each file is opened there without
// following a link and must be a regular file with a single name, belong to
// the account the data goes to and be no larger than the torrent says, and
// what it holds must be the torrent's: every piece it holds whole (together
// with the files it shares that piece with) matches the piece's hash, and at
// least one of those pieces is not a run of a single byte value. A file that
// only shares a name, or a block of zeros, with the torrent's is never
// written to. The file is then hard-linked into the private folder through
// a descriptor that is first found to be the very file that was checked,
// unchanged since (see FileId), never by name, and libtorrent checks it
// again: only pieces that verify are seeded; what is missing (past the end
// of the file, or in a piece shared with a file that is not there) is
// written into it. A later hard link the user makes to it only gives them
// torrent data.

// open_rel opens comps below dir without following a link: folders, then
// the last one as a file for reading. -1 and errno when it fails.
static Fd open_rel(int dir, std::vector<std::string> const &comps)
{
	Fd cur(::dup(dir));
	for (size_t i = 0; cur.ok() && i + 1 < comps.size(); ++i)
		cur = Fd(::openat(cur.fd, comps[i].c_str(), O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC));
	if (!cur.ok() || comps.empty())
		return Fd();
	return Fd(::openat(cur.fd, comps.back().c_str(), O_RDONLY | O_NOFOLLOW | O_NONBLOCK | O_NOCTTY | O_CLOEXEC));
}

// takeable: what a file in the user's folder must be to be taken over: a
// regular file of the owner (or, when no owner is named, an administrator's
// task, of root, which wrote it), no larger than the torrent's file, with a
// single name, or with a second one that is the private copy itself (taken
// over or handed over earlier: ours is the same inode).
static bool takeable(struct stat const &st, uid_t uid, bool root_too, std::int64_t max, bool ours)
{
	return S_ISREG(st.st_mode) && (st.st_nlink == 1 || (st.st_nlink == 2 && ours)) &&
		(st.st_uid == uid || (root_too && st.st_uid == 0)) && st.st_size <= max;
}

// stat_rel is fstatat of comps below dir, without following a link.
static bool stat_rel(int dir, std::vector<std::string> const &comps, struct stat &st)
{
	Fd cur(::dup(dir));
	for (size_t i = 0; cur.ok() && i + 1 < comps.size(); ++i)
		cur = Fd(::openat(cur.fd, comps[i].c_str(), O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC));
	return cur.ok() && !comps.empty() && ::fstatat(cur.fd, comps.back().c_str(), &st, AT_SYMLINK_NOFOLLOW) == 0;
}

// link_open hard-links the open file fd as name in dir.
static bool link_open(int fd, int dir, std::string const &name)
{
	if (::linkat(fd, "", dir, name.c_str(), AT_EMPTY_PATH) == 0)
		return true;
	char p[64];
	snprintf(p, sizeof p, "/proc/self/fd/%d", fd);
	return ::linkat(AT_FDCWD, p, dir, name.c_str(), AT_SYMLINK_FOLLOW) == 0;
}

// FileId is a file of the user's as it was found: whenever it is opened
// again (to read the next piece, or to link it) it must still be that file,
// unchanged. The inode number alone does not tell: a file system hands the
// number of a deleted file to the next new one at once. A new file differs
// in its generation (where the file system has one) and its change time; a
// change to the file (data, owner, links) moves its times and link count.
struct FileId
{
	dev_t dev = 0;
	ino_t ino = 0;
	nlink_t nlink = 0;
	off_t size = 0;
	std::int64_t mtime = 0, mtime_ns = 0, ctime = 0, ctime_ns = 0;
	long gen = 0;
	bool has_gen = false;

	bool operator==(FileId const &o) const
	{
		return dev == o.dev && ino == o.ino && nlink == o.nlink && size == o.size && mtime == o.mtime &&
			mtime_ns == o.mtime_ns && ctime == o.ctime && ctime_ns == o.ctime_ns && has_gen == o.has_gen && gen == o.gen;
	}
};

static FileId file_id(int fd, struct stat const &st)
{
	FileId r;
	r.dev = st.st_dev;
	r.ino = st.st_ino;
	r.nlink = st.st_nlink;
	r.size = st.st_size;
	r.mtime = st.st_mtim.tv_sec;
	r.mtime_ns = st.st_mtim.tv_nsec;
	r.ctime = st.st_ctim.tv_sec;
	r.ctime_ns = st.st_ctim.tv_nsec;
	long g = 0;
	if (::ioctl(fd, FS_IOC_GETVERSION, &g) == 0)
	{
		r.gen = g;
		r.has_gen = true;
	}
	return r;
}

// open_same opens comps below dir (see open_rel) when it is still the file
// id describes.
static Fd open_same(int dir, std::vector<std::string> const &comps, FileId const &id)
{
	Fd f = open_rel(dir, comps);
	struct stat st;
	if (!f.ok() || ::fstat(f.fd, &st) != 0 || !S_ISREG(st.st_mode) || !(file_id(f.fd, st) == id))
		return Fd();
	return f;
}

struct Adopted
{
	bool ok = false;
	int files = 0;
	std::string why;
};

// A file of the torrent found in the user's folder.
struct AdoptFile
{
	std::vector<std::string> src, dst; // below the private folder, below adopt_from
	FileId id;
	bool ours = false;     // the private copy's own inode (taken over or handed over earlier)
	bool linked = false;   // and the private folder has it already
	bool verified = false; // its data is the torrent's (ours, or checked)
};

// What adopt_check found; adopt_link takes it over.
struct AdoptCheck
{
	bool ok = false;
	std::string why;
	uid_t uid = 0;
	bool root_too = false;
	std::map<int, AdoptFile> files;
};

// What one check may cost before it gives up (the data then counts as data
// that cannot be checked, and nothing is taken over): bytes read, files
// opened and time.
static const std::int64_t adopt_max_bytes = std::int64_t(2) << 40;
static const std::int64_t adopt_max_opens = 2000000;
static const int adopt_max_hours = 6;

// adopt_check looks at the torrent's files in from/root (see above). It only
// reads (it runs away from the control loop) and stops when cancel is set.
// All or nothing: a file of the torrent that is there but does not qualify,
// or holds a piece that does not match, makes nothing taken over. Pieces are
// read in order, each at most once, and what one shows counts for every file
// it covers; a piece is only read when all its data is there, judged from
// the sizes before anything is opened: one that reaches past the end of a
// file or into a file that is not there counts neither way. A wanted file
// without a matching piece that is not a run of one byte value cannot be
// told by its data: it is left out (downloaded privately; the user's file is
// left alone). Files not wanted are read, never taken over, to complete the
// pieces they share with wanted ones (so they count like those).
// wanted: the priority of each file (0 = not wanted).
static AdoptCheck adopt_check(lt::torrent_info const &ti, std::string const &save, std::string const &from, std::string const &root,
	std::vector<int> const &wanted, int owner_uid, std::atomic<bool> const &cancel)
{
	AdoptCheck r;
	std::string err;
	if (!ti.info_hashes().has_v1())
	{
		r.why = "the torrent has no SHA-1 piece hashes";
		return r;
	}
	if (!plain_name(root))
	{
		r.why = "bad name: " + root;
		return r;
	}
	Fd d = open_dir(from, err);
	Fd s, seed;
	if (d.ok())
		s = open_dir(save, err);
	if (!d.ok() || !s.ok())
	{
		r.why = err;
		return r;
	}
	std::string serr;
	seed = open_dir(save + ".seed", serr); // where a hand-over left its copy (may be missing)
	struct stat dst, sst;
	if (::fstat(d.fd, &dst) != 0 || ::fstat(s.fd, &sst) != 0 || dst.st_dev != sst.st_dev)
	{
		r.why = "the temporary folder is on another volume than " + from;
		return r;
	}
	r.uid = owner_uid > 0 ? uid_t(owner_uid) : dst.st_uid;
	r.root_too = owner_uid <= 0;
	std::string const where = from + "/" + root;
	auto const until = std::chrono::steady_clock::now() + std::chrono::hours(adopt_max_hours);
	std::int64_t opens = 0, bytes = 0;
	// stopped: cancelled, or over the budget (why says which)
	auto stopped = [&]() -> bool {
		if (cancel.load())
			r.why = "the check was stopped";
		else if (opens > adopt_max_opens || bytes > adopt_max_bytes || std::chrono::steady_clock::now() > until)
			r.why = "the data in " + where + " is too large to be checked";
		else
			return false;
		return true;
	};
	lt::file_storage const &fs = ti.files();
	std::string top;
	{
		std::string p0 = fs.file_path(lt::file_index_t(0));
		size_t i = p0.find('/');
		top = i == std::string::npos ? p0 : p0.substr(0, i);
	}
	std::map<int, AdoptFile> extra; // not wanted, only read
	for (lt::file_index_t i : fs.file_range())
	{
		if (stopped())
			return r;
		int idx = static_cast<int>(i);
		std::int64_t size = fs.file_size(i);
		bool want = !(size_t(idx) < wanted.size() && wanted[size_t(idx)] == 0);
		if (fs.pad_file_at(i) || size == 0)
			continue;
		AdoptFile c;
		c.src = split_path(fs.file_path(i));
		c.dst = c.src;
		if (c.dst.empty() || c.dst[0] != top)
			continue;
		c.dst[0] = root;
		bool bad = false;
		for (auto const &n : c.src)
			bad = bad || !plain_name(n);
		if (bad)
			continue;
		++opens;
		Fd f = open_rel(d.fd, c.dst);
		if (!f.ok())
		{
			if (errno == ENOENT || !want)
				continue; // not there: downloaded
			r.why = where + ": " + fs.file_path(i) + " cannot be taken over";
			return r;
		}
		struct stat st, pst;
		if (::fstat(f.fd, &st) != 0)
		{
			if (!want)
				continue;
			r.why = where + ": " + fs.file_path(i) + " cannot be taken over";
			return r;
		}
		if (!want)
		{
			// Read only where it completes a piece of a wanted file
			if (takeable(st, r.uid, r.root_too, size, false))
			{
				c.id = file_id(f.fd, st);
				extra[idx] = c;
			}
			continue;
		}
		bool in_s = stat_rel(s.fd, c.src, pst);
		bool same = (in_s && pst.st_dev == st.st_dev && pst.st_ino == st.st_ino) ||
			(seed.ok() && stat_rel(seed.fd, c.src, pst) && pst.st_dev == st.st_dev && pst.st_ino == st.st_ino);
		if (in_s && !same)
			continue; // the private folder has a copy of its own: that one is used
		if (!takeable(st, r.uid, r.root_too, size, same))
		{
			r.why = where + ": " + fs.file_path(i) + " cannot be taken over";
			return r;
		}
		c.id = file_id(f.fd, st);
		c.ours = same;
		c.verified = same; // ours already
		c.linked = in_s && same;
		r.files[idx] = c;
	}
	if (r.files.empty())
	{
		r.why = "none of the torrent's files is in " + where;
		return r;
	}
	auto present = [&](int idx) -> AdoptFile const * {
		auto fi = r.files.find(idx);
		if (fi != r.files.end())
			return &fi->second;
		auto xi = extra.find(idx);
		return xi != extra.end() ? &xi->second : nullptr;
	};
	std::map<int, bool> told; // the files checked: whether a matching piece tells them by their data
	std::vector<char> buf(size_t(1) << 20);
	std::vector<char> const zeros(buf.size(), 0); // pad files hash as zeros
	int cur_idx = -1;
	Fd cur;
	int next = 0; // the pieces before it are done
	for (auto &kv : r.files)
	{
		if (kv.second.ours)
			continue;
		told.emplace(kv.first, false); // (a piece read for an earlier file may have told it already)
		lt::file_index_t fi(kv.first);
		std::int64_t have = std::min<std::int64_t>(kv.second.id.size, fs.file_size(fi));
		if (have <= 0)
			continue;
		int last = static_cast<int>(fs.map_file(fi, have - 1, 1).piece);
		for (int pc = std::max(next, static_cast<int>(fs.map_file(fi, 0, 0).piece)); pc <= last; ++pc)
		{
			if (stopped())
				return r;
			lt::piece_index_t p(pc);
			int psize = ti.piece_size(p);
			std::vector<lt::file_slice> slices = fs.map_block(p, 0, psize);
			bool whole = true;
			for (auto const &sl : slices)
			{
				if (fs.pad_file_at(sl.file_index))
					continue;
				AdoptFile const *c = present(static_cast<int>(sl.file_index));
				if (!c || sl.offset + sl.size > c->id.size)
				{
					whole = false;
					break;
				}
			}
			if (!whole)
				continue;
			bytes += psize;
			lt::hasher h;
			bool uniform = true, any = false;
			char first = 0;
			for (auto const &sl : slices)
			{
				int sidx = static_cast<int>(sl.file_index);
				bool pad = fs.pad_file_at(sl.file_index);
				if (!pad && sidx != cur_idx)
				{
					cur = Fd();
					cur_idx = -1;
					++opens;
					cur = open_same(d.fd, present(sidx)->dst, present(sidx)->id);
					if (!cur.ok())
					{
						r.why = where + ": " + fs.file_path(sl.file_index) + " changed while it was checked";
						return r;
					}
					cur_idx = sidx;
				}
				std::int64_t off = sl.offset, left = sl.size;
				while (left > 0)
				{
					if (stopped())
						return r;
					size_t n = size_t(std::min<std::int64_t>(left, std::int64_t(buf.size())));
					char const *data = zeros.data();
					if (!pad)
					{
						size_t got = 0;
						while (got < n)
						{
							ssize_t k = ::pread(cur.fd, buf.data() + got, n - got, off_t(off + std::int64_t(got)));
							if (k <= 0)
								break;
							got += size_t(k);
						}
						if (got != n)
						{
							r.why = where + ": " + fs.file_path(sl.file_index) + " changed while it was checked";
							return r;
						}
						data = buf.data();
					}
					h.update(data, int(n));
					if (uniform)
					{
						if (!any)
						{
							first = data[0];
							any = true;
						}
						uniform = data[0] == first && (n < 2 || std::memcmp(data, data + 1, n - 1) == 0);
					}
					off += std::int64_t(n);
					left -= std::int64_t(n);
				}
			}
			bool match = h.final() == ti.hash_for_piece(p);
			for (auto const &sl : slices)
			{
				auto ci = r.files.find(static_cast<int>(sl.file_index));
				if (ci == r.files.end() || ci->second.ours)
					continue;
				if (!match)
				{
					r.why = where + ": " + fs.file_path(sl.file_index) + " differs from the torrent";
					return r;
				}
				if (!uniform)
					told[ci->first] = true;
			}
		}
		next = std::max(next, last + 1);
	}
	cur = Fd();
	for (auto const &kv : told)
	{
		if (kv.second)
			r.files[kv.first].verified = true;
		else
			r.files.erase(kv.first);
	}
	if (r.files.empty())
	{
		r.why = "no file in " + where + " can be checked against the torrent";
		return r;
	}
	r.ok = true;
	return r;
}

// adopt_link links what adopt_check found into the private folder save,
// each through a descriptor of the file that was checked (see FileId),
// unchanged and still takeable, never by name. Undone when one fails.
static Adopted adopt_link(lt::torrent_info const &ti, std::string const &save, std::string const &from, AdoptCheck const &chk)
{
	Adopted r;
	std::string err;
	Fd d = open_dir(from, err);
	Fd s;
	if (d.ok())
		s = open_dir(save, err);
	if (!d.ok() || !s.ok())
	{
		r.why = err;
		return r;
	}
	lt::file_storage const &fs = ti.files();
	std::vector<std::pair<std::vector<std::string>, std::string>> made; // folder, name (to undo)
	auto undo = [&]() {
		for (auto const &m : made)
		{
			Fd u(::dup(s.fd));
			for (size_t i = 0; u.ok() && i < m.first.size(); ++i)
				u = Fd(::openat(u.fd, m.first[i].c_str(), O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC));
			if (u.ok())
				::unlinkat(u.fd, m.second.c_str(), 0);
		}
	};
	for (auto const &kv : chk.files)
	{
		AdoptFile const &c = kv.second;
		std::string what = from + "/" + c.dst[0] + ": " + fs.file_path(lt::file_index_t(kv.first)) + " cannot be taken over";
		if (c.linked)
		{
			++r.files;
			continue;
		}
		Fd f = open_same(d.fd, c.dst, c.id);
		struct stat st;
		if (!f.ok() || ::fstat(f.fd, &st) != 0 ||
			!takeable(st, chk.uid, chk.root_too, fs.file_size(lt::file_index_t(kv.first)), c.ours))
		{
			r.why = what + " (it changed since it was checked)";
			undo();
			return r;
		}
		Fd at(::dup(s.fd));
		for (size_t i = 0; at.ok() && i + 1 < c.src.size(); ++i)
		{
			::mkdirat(at.fd, c.src[i].c_str(), 0700);
			at = Fd(::openat(at.fd, c.src[i].c_str(), O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC));
		}
		if (!at.ok() || !link_open(f.fd, at.fd, c.src.back()))
		{
			r.why = what;
			undo();
			return r;
		}
		made.emplace_back(std::vector<std::string>(c.src.begin(), c.src.end() - 1), c.src.back());
		++r.files;
	}
	r.ok = true;
	return r;
}

// A check of data in the destination, running away from the control loop.
struct AdoptJob
{
	std::mutex mu;
	bool done = false;
	AdoptCheck res;
	std::string from, root;
	std::atomic<bool> cancel{false}; // the torrent was removed, or is to be taken from elsewhere
};

// The checks run in threads of their own: at most two at a time, one per
// account (no owner: one), so that one account cannot hold up the others'.
// A thread holds its slot until it ends, also after its torrent was removed
// (which stops it soon).
static std::mutex adopt_mu;
static int adopt_running = 0;
static std::set<int> adopt_owners;

static bool adopt_slot(int owner)
{
	std::lock_guard<std::mutex> l(adopt_mu);
	if (adopt_running >= 2 || adopt_owners.count(owner))
		return false;
	++adopt_running;
	adopt_owners.insert(owner);
	return true;
}

static void adopt_slot_free(int owner)
{
	std::lock_guard<std::mutex> l(adopt_mu);
	--adopt_running;
	adopt_owners.erase(owner);
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
	bool untrusted = false;   // the destination folder is a regular user's (untrusted_dir)
	bool untrusted_known = true; // the saved options said (before 1.5 they did not; not saved)
	std::string handed_to;    // untrusted: the folder the data was handed over to
	std::string handed_root;  // ... and the name of its top folder (or single file) there
	std::string seed_dir;     // untrusted: the private folder it seeds from after the hand-over
	int owner_uid = -1, owner_gid = -1; // untrusted: who the handed-over files go to (-1: the destination's owner)
	bool handed = false;      // the hand-over is complete (seed_dir holds the data)
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
	j["untrusted"] = o.untrusted;
	if (!o.handed_to.empty())
	{
		j["handed_to"] = o.handed_to;
		j["handed_root"] = o.handed_root;
	}
	if (!o.seed_dir.empty())
		j["seed_dir"] = o.seed_dir;
	if (o.owner_uid > 0)
	{
		j["owner_uid"] = o.owner_uid;
		j["owner_gid"] = o.owner_gid;
	}
	j["handed"] = o.handed;
	if (o.has_select)
		j["select"] = o.select;
	json pr = json::object();
	for (auto &kv : o.priorities)
		pr[std::to_string(kv.first)] = kv.second;
	j["priorities"] = pr;
	return j;
}

// jget reads a field of a type, or gives def (never throws on a wrong type).
template <class T>
static T jget(json const &j, char const *key, T def)
{
	if (!j.is_object())
		return def;
	auto it = j.find(key);
	if (it == j.end() || it->is_null())
		return def;
	try
	{
		return it->get<T>();
	}
	catch (...)
	{
		return def;
	}
}

static Options opt_from(json const &j)
{
	Options o;
	o.seed_ratio = jget(j, "seed_ratio", 0.0);
	o.seed_time = jget(j, "seed_time", 0);
	o.complete = jget(j, "complete", false);
	o.guard = jget(j, "guard", false);
	o.guard_known = j.contains("guard");
	o.untrusted = jget(j, "untrusted", false);
	o.untrusted_known = j.contains("untrusted");
	o.handed_to = jget(j, "handed_to", std::string());
	o.handed_root = jget(j, "handed_root", std::string());
	o.seed_dir = jget(j, "seed_dir", std::string());
	o.handed = jget(j, "handed", false);
	o.owner_uid = jget(j, "owner_uid", -1);
	o.owner_gid = jget(j, "owner_gid", -1);
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

// --- the low memory profile ---
//
// dcd decides the profile from the NAS's memory (memory_profile "low" on
// models with 1.5 GB or less) and sends it with the settings. The values
// follow libtorrent's min_memory_usage() where it saves memory without
// breaking anything dc-bt relies on: not its alert queue of 100 (resume data
// and moves would be lost), its one-block disk queue and send buffer (no
// throughput left), disable_os_cache (seeding would read every block from
// disk) or 5 connection attempts a second.
static bool low_memory(json const &s) { return s.value("memory_profile", std::string()) == "low"; }

static void apply_low_memory(lt::settings_pack &p, Tuning const &t)
{
	p.set_int(lt::settings_pack::max_peer_recv_buffer_size, 256 * 1024); // default 2 MiB
	p.set_int(lt::settings_pack::send_buffer_watermark, 128 * 1024);    // default 500 KiB
	p.set_int(lt::settings_pack::recv_socket_buffer_size, 64 * 1024);   // default: the system's
	p.set_int(lt::settings_pack::send_socket_buffer_size, 64 * 1024);
	p.set_int(lt::settings_pack::max_peerlist_size, 500);            // default 3000
	p.set_int(lt::settings_pack::max_paused_peerlist_size, 50);      // default 1000
	p.set_int(lt::settings_pack::file_pool_size, 10);                // default 40
	p.set_int(lt::settings_pack::max_out_request_queue, 300);        // default 500
	p.set_int(lt::settings_pack::max_allowed_in_request_queue, 250); // default 2000
	p.set_int(lt::settings_pack::connection_speed, 10);              // default 30 a second
	// fewer partly downloaded pieces to keep track of
	p.set_int(lt::settings_pack::whole_pieces_threshold, 2);
	p.set_bool(lt::settings_pack::prioritize_partial_pieces, true);
	p.set_int(lt::settings_pack::active_checking, 1);
	p.set_int(lt::settings_pack::alert_queue_size, 2000);
	// The disk tuning of a large NAS (64-bit back end; also when the profile
	// is forced on one) at most libtorrent's defaults
	p.set_int(lt::settings_pack::max_queued_disk_bytes, std::min(t.max_queued_disk_bytes, 1024 * 1024));
	p.set_int(lt::settings_pack::checking_mem_usage, std::min(t.checking_mem_usage, 256));
}

// restore_memory_defaults puts back libtorrent's defaults of what the low
// profile changes (settings left out of a pack keep the session's values).
static void restore_memory_defaults(lt::settings_pack &p)
{
	static const int ints[] = {lt::settings_pack::max_peer_recv_buffer_size, lt::settings_pack::send_buffer_watermark,
		lt::settings_pack::recv_socket_buffer_size, lt::settings_pack::send_socket_buffer_size,
		lt::settings_pack::max_peerlist_size, lt::settings_pack::max_paused_peerlist_size, lt::settings_pack::file_pool_size,
		lt::settings_pack::max_out_request_queue, lt::settings_pack::max_allowed_in_request_queue,
		lt::settings_pack::connection_speed, lt::settings_pack::whole_pieces_threshold};
	static lt::settings_pack const def = lt::default_settings();
	for (int k : ints)
		p.set_int(k, def.get_int(k));
	p.set_bool(lt::settings_pack::prioritize_partial_pieces, def.get_bool(lt::settings_pack::prioritize_partial_pieces));
}

static json low_memory_json()
{
	return json{{"max_peer_recv_buffer_size", 256 * 1024}, {"send_buffer_watermark", 128 * 1024},
		{"socket_buffer_size", 64 * 1024}, {"max_peerlist_size", 500}, {"max_paused_peerlist_size", 50},
		{"file_pool_size", 10}, {"connection_speed", 10}, {"active_checking", 1}, {"alert_queue_size", 2000}};
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
	// What dcd has not read yet: dc-bt never waits for it, and a client that
	// stops reading is dropped once this much waits
	std::string outbuf;
	size_t outoff = 0;
	static constexpr size_t max_out = size_t(256) << 20;
	int pending_saves = 0;
	// status_all's changed answers (dc-bt 1.8): the torrents that may have
	// changed since the last answer, what was last sent of each, and whether
	// the connected dcd has had a full answer
	std::set<std::string> changed_keys;
	std::map<std::string, std::string> sent_sig;
	std::map<std::string, int> sent_pieces;
	bool full_sent = false;
	bool got_state_update = false;
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
	std::map<std::string, std::shared_ptr<AdoptJob>> adopt_jobs; // checks of data in the destination (see adopt_check)
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

	// Web seeds of guarded torrents given by name, looked up away from the
	// loop: one that leads to the NAS, or to the tracker proxy the filter
	// lets through (at any port), is removed. Shared with the lookups.
	struct SeedCheck
	{
		std::mutex mu;
		std::vector<std::pair<std::string, std::string>> bad; // infohash, url
	};
	std::shared_ptr<SeedCheck> seedchk = std::make_shared<SeedCheck>();

	void check_web_seeds(lt::torrent_handle const &h)
	{
		std::vector<std::string> names;
		for (auto const &u : h.url_seeds())
			names.push_back(u);
		for (auto const &u : h.http_seeds())
			names.push_back(u);
		if (names.empty())
			return;
		std::vector<lt::address> deny = own;
		deny.insert(deny.end(), exempt.begin(), exempt.end());
		std::string key = key_of(h.info_hashes());
		std::shared_ptr<SeedCheck> st = seedchk;
		std::thread([st, key, names, deny]() {
			for (auto const &url : names)
			{
				lt::error_code ec;
				std::string host;
				std::tie(std::ignore, std::ignore, host, std::ignore, std::ignore) = lt::parse_url_components(url, ec);
				lt::address lit;
				if (ec || host.empty() || literal_address(host, lit))
					continue;
				struct addrinfo hints, *res = nullptr;
				std::memset(&hints, 0, sizeof hints);
				hints.ai_socktype = SOCK_STREAM;
				if (getaddrinfo(host.c_str(), nullptr, &hints, &res) != 0)
					continue;
				bool bad = false;
				for (struct addrinfo *i = res; i && !bad; i = i->ai_next)
				{
					lt::address a;
					if (i->ai_family == AF_INET)
					{
						lt::address_v4::bytes_type b;
						std::memcpy(b.data(), &reinterpret_cast<struct sockaddr_in *>(i->ai_addr)->sin_addr, 4);
						a = lt::address_v4(b);
					}
					else if (i->ai_family == AF_INET6)
					{
						lt::address_v6::bytes_type b;
						std::memcpy(b.data(), &reinterpret_cast<struct sockaddr_in6 *>(i->ai_addr)->sin6_addr, 16);
						a = lt::address_v6(b);
					}
					else
						continue;
					bad = forbidden_addr(a, deny) ||
						(a.is_v6() && a.to_v6().is_v4_mapped() && forbidden_addr(lt::make_address_v4(lt::v4_mapped, a.to_v6()), deny));
				}
				freeaddrinfo(res);
				if (bad)
				{
					std::lock_guard<std::mutex> l(st->mu);
					st->bad.emplace_back(key, url);
				}
			}
		}).detach();
	}

	// drop_bad_seeds removes what check_web_seeds found.
	void drop_bad_seeds()
	{
		std::vector<std::pair<std::string, std::string>> bad;
		{
			std::lock_guard<std::mutex> l(seedchk->mu);
			bad.swap(seedchk->bad);
		}
		for (auto const &b : bad)
		{
			auto it = handles.find(b.first);
			if (it == handles.end() || !it->second.is_valid() || !opts[b.first].guard)
				continue;
			it->second.remove_url_seed(b.second);
			it->second.remove_http_seed(b.second);
			dlog("%s: web seed removed: its name leads to the NAS", b.first.c_str());
		}
	}

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
		check_web_seeds(h);
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

	void drop_client()
	{
		if (client_fd >= 0)
			::close(client_fd);
		client_fd = -1;
		inbuf.clear();
		outbuf.clear();
		outoff = 0;
	}

	// flush_out sends what the socket takes now.
	void flush_out()
	{
		while (client_fd >= 0 && outoff < outbuf.size())
		{
			ssize_t n = ::send(client_fd, outbuf.data() + outoff, outbuf.size() - outoff, MSG_NOSIGNAL | MSG_DONTWAIT);
			if (n > 0)
			{
				outoff += size_t(n);
				continue;
			}
			if (n < 0 && errno == EINTR)
				continue;
			if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK))
				break;
			drop_client();
			return;
		}
		if (outoff == outbuf.size())
		{
			outbuf.clear();
			outoff = 0;
		}
		else if (outoff > (size_t(1) << 20))
		{
			outbuf.erase(0, outoff);
			outoff = 0;
		}
	}

	void send_line(json const &j)
	{
		if (client_fd < 0)
			return;
		std::string s = j.dump(-1, ' ', false, json::error_handler_t::replace);
		s.push_back('\n');
		if (outbuf.size() - outoff + s.size() > max_out)
		{
			dlog("dcd does not read its answers: connection dropped");
			drop_client();
			return;
		}
		outbuf += s;
		flush_out();
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
		bool utp = true, incoming = true, outgoing = true;
		json px = s.value("proxy", json::object());
		std::string ptype = px.value("type", std::string("none"));
		std::string host = px.value("host", std::string());
		bool force = px.value("force", true) && ptype != "none";
		if (ptype == "none" || (host.empty() && !force))
		{
			p.set_int(lt::settings_pack::proxy_type, lt::settings_pack::none);
		}
		else
		{
			std::string user = px.value("user", std::string()), pass = px.value("pass", std::string());
			int port = px.value("port", 0);
			bool peers = px.value("peers", true), trackers = px.value("trackers", true);
			int t = lt::settings_pack::none;
			if (ptype == "socks5")
				t = user.empty() ? lt::settings_pack::socks5 : lt::settings_pack::socks5_pw;
			else if (ptype == "http")
				t = user.empty() ? lt::settings_pack::http : lt::settings_pack::http_pw;
			if (host.empty() || t == lt::settings_pack::none)
			{
				// No proxy to use while nothing may go direct: every
				// connection goes to a port where nothing listens
				t = lt::settings_pack::socks5;
				host = "127.0.0.1";
				port = 1;
				user.clear();
				pass.clear();
				peers = trackers = true;
			}
			p.set_int(lt::settings_pack::proxy_type, t);
			p.set_str(lt::settings_pack::proxy_hostname, host);
			p.set_int(lt::settings_pack::proxy_port, port);
			p.set_str(lt::settings_pack::proxy_username, user);
			p.set_str(lt::settings_pack::proxy_password, pass);
			p.set_bool(lt::settings_pack::proxy_hostnames, px.value("hostnames", true));
			if (ptype == "http")
				utp = false; // an HTTP proxy cannot carry UDP
			if (force)
			{
				// Never connect directly: no incoming peers, no port mapping,
				// no local discovery, no UDP that might bypass the proxy.
				// Trackers and web seeds go through the proxy; peers only when
				// the proxy carries them (an HTTP proxy is only offered for
				// trackers), otherwise there are none
				incoming = false;
				upnp = false;
				lsd = false;
				dht = false;
				utp = false;
				if (!peers || t == lt::settings_pack::http || t == lt::settings_pack::http_pw)
					outgoing = false;
				peers = trackers = true;
			}
			p.set_bool(lt::settings_pack::proxy_peer_connections, peers);
			p.set_bool(lt::settings_pack::proxy_tracker_connections, trackers);
		}
		p.set_bool(lt::settings_pack::enable_dht, dht);
		dht_on = dht;
		p.set_bool(lt::settings_pack::enable_lsd, lsd);
		p.set_bool(lt::settings_pack::enable_upnp, upnp);
		p.set_bool(lt::settings_pack::enable_natpmp, upnp);
		p.set_bool(lt::settings_pack::enable_outgoing_utp, utp);
		p.set_bool(lt::settings_pack::enable_incoming_utp, utp && incoming);
		p.set_bool(lt::settings_pack::enable_incoming_tcp, incoming);
		p.set_bool(lt::settings_pack::enable_outgoing_tcp, outgoing);
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
		if (low_memory(s))
			apply_low_memory(p, tune);
		else
			restore_memory_defaults(p);
		return p;
	}

	// What the session is tuned for: the hardware and the memory profile
	json tuning_json()
	{
		json j = hardware_json(hw, tune);
		bool low = low_memory(settings);
		j["memory_profile"] = low ? "low" : "normal";
		if (low)
			j["low_memory"] = low_memory_json();
		// What the session has now, read back from libtorrent
		if (ses)
		{
			lt::settings_pack cur = ses->get_settings();
			j["session"] = json{{"connections_limit", cur.get_int(lt::settings_pack::connections_limit)},
				{"active_checking", cur.get_int(lt::settings_pack::active_checking)},
				{"alert_queue_size", cur.get_int(lt::settings_pack::alert_queue_size)},
				{"max_peer_recv_buffer_size", cur.get_int(lt::settings_pack::max_peer_recv_buffer_size)},
				{"send_buffer_watermark", cur.get_int(lt::settings_pack::send_buffer_watermark)},
				{"max_peerlist_size", cur.get_int(lt::settings_pack::max_peerlist_size)},
				{"file_pool_size", cur.get_int(lt::settings_pack::file_pool_size)},
				{"max_queued_disk_bytes", cur.get_int(lt::settings_pack::max_queued_disk_bytes)},
				{"checking_mem_usage", cur.get_int(lt::settings_pack::checking_mem_usage)}};
		}
		return j;
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
			// Without readable options nothing is known: whose torrent it is
			// (guard) and whether its folder is a regular user's
			Options o;
			o.guard_known = o.untrusted_known = false;
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
			// A regular user's torrent runs only from a folder only root can
			// change. One saved before 1.5, or without readable options, is
			// left alone until dcd adds it again and says whose it is
			std::string perr;
			if (!o.metadata_only && (!o.untrusted_known || (o.untrusted && !private_dir(atp.save_path, perr))))
			{
				dlog("restore %s: waits for dcd%s%s", k.c_str(), perr.empty() ? "" : ": ", perr.c_str());
				continue;
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

	// key_arg is the infohash of a request: 40 lower-case hex digits, as
	// key_of makes them (it names files in the state folder)
	static std::string key_arg(json const &req)
	{
		std::string k = jget(req, "infohash", std::string());
		bool ok = k.size() == 40;
		for (char c : k)
			ok = ok && ((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'));
		if (!ok)
			throw std::runtime_error("torrent not found");
		return k;
	}

	lt::torrent_handle find(json const &req)
	{
		std::string k = key_arg(req);
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

	Options load_opts(std::string const &k)
	{
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
		return o;
	}

	// resume_finished: the saved resume data of k has every piece of the
	// files it wants (their size in wanted).
	bool resume_finished(std::string const &k, std::shared_ptr<lt::torrent_info> ti, std::int64_t &wanted)
	{
		std::vector<char> buf;
		if (!read_file(resume_path(k), buf))
			return false;
		lt::error_code ec;
		lt::add_torrent_params p = lt::read_resume_data(buf, ec);
		if (ec)
			return false;
		if (!p.ti)
			p.ti = ti;
		if (!p.ti || !p.ti->is_valid())
			return false;
		lt::file_storage const &fs = p.ti->files();
		wanted = 0;
		for (lt::file_index_t i : fs.file_range())
		{
			int idx = static_cast<int>(i);
			auto prio = size_t(idx) < p.file_priorities.size() ? p.file_priorities[size_t(idx)] : lt::default_priority;
			std::int64_t size = fs.file_size(i);
			if (fs.pad_file_at(i) || prio == lt::dont_download || size == 0)
				continue;
			wanted += size;
			int first = static_cast<int>(fs.map_file(i, 0, 0).piece);
			int last = static_cast<int>(fs.map_file(i, size - 1, 1).piece);
			for (int pc = first; pc <= last; ++pc)
				if (pc >= p.have_pieces.size() || !p.have_pieces.get_bit(lt::piece_index_t(pc)))
					return false;
		}
		return true;
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
		o.untrusted = req.value("untrusted_dir", false);
		o.owner_uid = req.value("owner_uid", -1);
		o.owner_gid = req.value("owner_gid", -1);
		read_select(req, o);

		auto ex = handles.find(key);
		if (ex != handles.end() && ex->second.is_valid() && o.untrusted && !o.metadata_only && !opts[key].metadata_only)
		{
			// A regular user's torrent only runs from a folder only root can
			// change. One that runs elsewhere (added for an administrator, or
			// before 1.5) stops there and is taken as added anew
			Options &eo = opts[key];
			std::string cur = ex->second.status(lt::torrent_handle::query_save_path).save_path, perr;
			if (!private_dir(cur, perr))
			{
				dlog("%s: stops running from %s (%s)", key.c_str(), cur.c_str(), perr.c_str());
				ses->remove_torrent(ex->second);
				handles.erase(key);
				opts.erase(key);
				forget_found(key);
				ex = handles.end();
			}
			else if (!eo.handed && cur != save && (eo.seed_dir.empty() || cur != eo.seed_dir) && private_dir(save, perr))
			{
				// dcd moved the unfinished data to another such folder
				ex->second.move_storage(save, lt::move_flags_t::reset_save_path);
			}
		}
		if (o.untrusted && !o.metadata_only && (ex == handles.end() || !ex->second.is_valid()))
		{
			std::string perr;
			if (!private_dir(save, perr))
			{
				// dcd believes the data is in the user's folder, where dc-bt
				// never reads or writes: as far as dc-bt goes its seeding is over
				std::int64_t wanted = 0;
				if (resume_finished(key, ti, wanted))
				{
					Options so = load_opts(key);
					so.untrusted = so.untrusted_known = true;
					std::string js = opt_json(so).dump();
					write_file(opts_path(key), js.data(), js.size());
					dlog("%s: not started from %s (%s): its seeding counts as finished", key.c_str(), save.c_str(), perr.c_str());
					return json{{"infohash", key}, {"existed", false}, {"resumed", false}, {"seed_done", true}, {"total_wanted", wanted}};
				}
				throw std::runtime_error("not a private folder: " + perr);
			}
		}
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
			if (!o.metadata_only && (eo.guard != o.guard || !eo.guard_known || eo.untrusted != o.untrusted || !eo.untrusted_known ||
				eo.owner_uid != o.owner_uid || eo.owner_gid != o.owner_gid))
			{
				eo.guard = o.guard;
				eo.guard_known = true;
				eo.untrusted = o.untrusted;
				eo.untrusted_known = true;
				eo.owner_uid = o.owner_uid;
				eo.owner_gid = o.owner_gid;
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
		// Data already in the user's folder (see adopt): taken over, then
		// checked; the torrent then counts as handed over to that folder
		int adopted = 0;
		std::string adopt_from = req.value("adopt_from", std::string()), adopt_root = req.value("adopt_root", std::string());
		if (o.untrusted && !o.metadata_only && !adopt_from.empty() && !resumed)
		{
			Adopted a;
			if (ti)
			{
				if (adopt_root.empty())
					adopt_root = ti->name();
				int n = ti->num_files();
				std::vector<int> wanted(size_t(n), o.has_select ? 0 : 4);
				for (int i : o.select)
					if (i >= 0 && i < n)
						wanted[size_t(i)] = 4;
				for (auto &kv : o.priorities)
					if (kv.first >= 0 && kv.first < n)
						wanted[size_t(kv.first)] = kv.second;
				// The check reads and hashes data: it runs away from the
				// control loop (see adopt_slot), and dcd adds the torrent
				// again until it is done
				auto ji = adopt_jobs.find(key);
				if (ji != adopt_jobs.end() && (ji->second->from != adopt_from || ji->second->root != adopt_root))
				{
					ji->second->cancel = true;
					adopt_jobs.erase(ji);
					ji = adopt_jobs.end();
				}
				if (ji == adopt_jobs.end())
				{
					int slot = o.owner_uid > 0 ? o.owner_uid : 0;
					if (adopt_slot(slot))
					{
						auto job = std::make_shared<AdoptJob>();
						job->from = adopt_from;
						job->root = adopt_root;
						adopt_jobs[key] = job;
						std::shared_ptr<lt::torrent_info const> tic = ti;
						int owner = o.owner_uid;
						try
						{
							std::thread([job, tic, save, adopt_from, adopt_root, wanted, owner, slot]() {
								struct Held
								{
									int slot;
									~Held() { adopt_slot_free(slot); }
								} held{slot};
								pthread_setname_np(pthread_self(), "dc-bt adopt");
								AdoptCheck r;
								try
								{
									r = adopt_check(*tic, save, adopt_from, adopt_root, wanted, owner, job->cancel);
								}
								catch (std::exception const &e)
								{
									r = AdoptCheck();
									r.why = e.what();
								}
								catch (...)
								{
									r = AdoptCheck();
									r.why = "the check failed";
								}
								std::lock_guard<std::mutex> l(job->mu);
								job->res = std::move(r);
								job->done = true;
							}).detach();
						}
						catch (...)
						{
							adopt_slot_free(slot);
							adopt_jobs.erase(key);
							throw;
						}
					}
					return json{{"infohash", key}, {"adopting", true}};
				}
				std::shared_ptr<AdoptJob> job = ji->second;
				{
					std::lock_guard<std::mutex> l(job->mu);
					if (!job->done)
						return json{{"infohash", key}, {"adopting", true}};
				}
				adopt_jobs.erase(ji);
				if (job->res.ok)
					a = adopt_link(*ti, save, adopt_from, job->res);
				else
					a.why = job->res.why;
			}
			else
				a.why = "the file list is not known yet";
			if (a.ok)
			{
				adopted = a.files;
				check = true;
				o.handed = true;
				o.handed_to = adopt_from;
				o.handed_root = adopt_root;
				o.seed_dir = save;
				dlog("%s: %d files taken over from %s/%s", key.c_str(), adopted, adopt_from.c_str(), adopt_root.c_str());
			}
			else
			{
				dlog("%s: data in %s not taken over: %s", key.c_str(), adopt_from.c_str(), a.why.c_str());
				if (req.value("adopt_required", false))
					throw std::runtime_error("cannot take over the data: " + a.why);
			}
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
		if (ti && !root.empty() && adopted == 0)
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
		return json{{"infohash", key}, {"existed", false}, {"resumed", resumed}, {"adopted", adopted}};
	}

	// Move the data to another folder; the torrent keeps running and reads
	// from there once libtorrent is done (storage_moved). Nothing that already
	// exists at the destination is replaced: the move fails instead.
	// top_of: the top folder (or the single file) of a torrent's data as
	// libtorrent has it now.
	static std::string top_of(lt::file_storage const &fs)
	{
		std::string p = fs.num_files() > 0 ? fs.file_path(lt::file_index_t(0)) : std::string();
		size_t i = p.find('/');
		return i == std::string::npos ? p : p.substr(0, i);
	}

	// hand_files lists the complete, wanted files of a torrent: where they are
	// below its save path and where they go below the destination (the top
	// renamed to root).
	std::vector<HandFile> hand_files(lt::torrent_handle const &h, std::shared_ptr<lt::torrent_info const> ti, std::string const &root)
	{
		lt::file_storage const &fs = ti->files();
		std::string top = top_of(fs);
		std::vector<std::int64_t> prog;
		h.file_progress(prog, lt::torrent_handle::piece_granularity);
		std::vector<lt::download_priority_t> pr = h.get_file_priorities();
		std::vector<HandFile> out;
		for (lt::file_index_t i : fs.file_range())
		{
			int idx = static_cast<int>(i);
			if (fs.pad_file_at(i) || (size_t(idx) < pr.size() && pr[size_t(idx)] == lt::dont_download))
				continue;
			std::int64_t size = fs.file_size(i);
			if (size_t(idx) >= prog.size() || prog[size_t(idx)] < size)
				continue;
			HandFile f;
			f.src = split_path(fs.file_path(i));
			f.dst = f.src;
			if (!f.dst.empty() && f.dst[0] == top)
				f.dst[0] = root;
			f.empty = size == 0;
			out.push_back(f);
		}
		// One folder after the other: each is entered once
		std::sort(out.begin(), out.end(), [](HandFile const &a, HandFile const &b) { return a.dst < b.dst; });
		return out;
	}

	// move_untrusted hands a regular user's data over to dst (see hand_over)
	// and keeps seeding from a private folder beside the temporary one, which
	// dcd deletes once the move is reported done.
	json move_untrusted(lt::torrent_handle const &h, std::string const &k, std::shared_ptr<lt::torrent_info const> ti,
		std::string const &dst, std::string const &root)
	{
		Options &o = opts[k];
		std::string top = top_of(ti->files());
		std::string want = root.empty() ? top : root;
		auto failed = [&](std::string const &msg) {
			o.move_error = msg;
			dlog("move %s failed: %s", k.c_str(), msg.c_str());
			event(json{{"event", "storage_move_failed"}, {"infohash", k}, {"error", msg}});
			return json::object();
		};
		if (!plain_name(want))
			return failed("bad name: " + want);
		lt::torrent_status ts = h.status(lt::torrent_handle::query_save_path);
		if (ts.moving_storage)
			return json::object(); // on its way to its own folder
		std::string cur = ts.save_path, perr;
		while (cur.size() > 1 && cur.back() == '/')
			cur.pop_back();
		if (!o.handed && !o.seed_dir.empty() && cur == o.seed_dir)
		{
			o.handed = true; // moved, before the alert said so
			save_opts(k);
		}
		if (o.handed && o.handed_to == dst && o.handed_root == want)
			return json::object(); // there already
		if (!private_dir(cur, perr))
			return failed(perr);
		o.move_error.clear();
		std::vector<std::string> skipped;
		std::string err = hand_over(cur, dst, hand_files(h, ti, want), false, o.owner_uid, o.owner_gid, skipped);
		if (!err.empty())
			return failed(err);
		o.handed_to = dst;
		o.handed_root = want;
		if (o.handed)
		{
			// Seeding from its own folder already: handed over once more
			save_opts(k);
			event(json{{"event", "storage_moved"}, {"infohash", k}, {"save_path", dst}});
			return json::object();
		}
		std::string seed = cur + ".seed";
		Fd parent = open_dir(dir_name(seed), perr);
		if (!parent.ok())
			return failed(perr);
		remove_tree(parent.fd, base_name(seed)); // what an interrupted move left
		// Made here: libtorrent would make it writable by the group
		if (::mkdirat(parent.fd, base_name(seed).c_str(), 0700) != 0)
			return failed(seed + ": " + strerror(errno));
		o.seed_dir = seed;
		save_opts(k);
		h.move_storage(seed, lt::move_flags_t::fail_if_exist);
		return json::object();
	}

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
		if (opts[k].untrusted)
			return move_untrusted(h, k, ti, dst, root);
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
		if (k.size() != 40 || k.find_first_not_of("0123456789abcdef") != std::string::npos)
			return;
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
		// A regular user's data that was handed over: dcd sees it where it
		// was handed to (dc-bt seeds from its own folder)
		bool handed = o.handed && !o.handed_to.empty();
		j["save_path"] = handed ? o.handed_to : s.save_path;
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
			j["pieces_done"] = s.num_pieces;
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
				std::string top = handed ? top_of(fs) : std::string();
				for (lt::file_index_t i : fs.file_range())
				{
					int idx = int(static_cast<int>(i));
					json f;
					f["index"] = idx;
					std::string path = fs.file_path(i);
					if (handed && !o.handed_root.empty() && o.handed_root != top &&
						(path == top || path.compare(0, top.size() + 1, top + "/") == 0))
						path = o.handed_root + path.substr(top.size());
					f["path"] = path;
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

	// What of a torrent's own options shows in its status: a change of it is
	// a change of the torrent for the changed answers
	static std::string options_sig(Options const &o)
	{
		return std::string(o.complete ? "c" : "-") + (o.handed ? "h" : "-") + "|" + o.handed_to + "|" + o.handed_root + "|" + o.move_error;
	}

	// changed_since asks libtorrent which torrents changed since the last
	// call (post_torrent_updates answers at once, also when nothing did) and
	// waits for its answer while handling the other alerts. False when it does
	// not come within a second.
	bool changed_since()
	{
		got_state_update = false;
		ses->post_torrent_updates(lt::status_flags_t{});
		auto deadline = clk::now() + std::chrono::seconds(1);
		while (!got_state_update && clk::now() < deadline)
		{
			ses->wait_for_alert(std::chrono::milliseconds(50));
			handle_alerts();
		}
		return got_state_update;
	}

	// status_all answers every torrent; with "changed": true (dc-bt 1.8),
	// after a first full answer to the connected dcd and unless "full" is set,
	// only those that changed since the last answer, with "present" listing
	// every torrent so that dcd can drop the ones that are gone.
	json cmd_status_all(json const &req)
	{
		bool files = req.value("files", true);
		bool changed = req.value("changed", false);
		bool full = !changed || req.value("full", false) || !full_sent || !changed_since();
		json arr = json::array(), present = json::array();
		for (auto &kv : handles)
		{
			if (!kv.second.is_valid() || opts[kv.first].metadata_only)
				continue;
			present.push_back(kv.first);
			std::string sig = options_sig(opts[kv.first]);
			auto ss = sent_sig.find(kv.first);
			if (!full && !changed_keys.count(kv.first) && ss != sent_sig.end() && ss->second == sig)
				continue;
			try
			{
				json j = status_of(kv.first, kv.second, files);
				// The pieces only when more are done than dcd was last sent
				if (changed && j.contains("pieces_done"))
				{
					int done = j["pieces_done"].get<int>();
					auto sp = sent_pieces.find(kv.first);
					if (!full && sp != sent_pieces.end() && sp->second == done)
						j.erase("pieces");
					sent_pieces[kv.first] = done;
				}
				arr.push_back(std::move(j));
				sent_sig[kv.first] = sig;
			}
			catch (std::exception const &e)
			{
				dlog("status %s: %s", kv.first.c_str(), e.what());
			}
		}
		if (!changed)
			return json{{"torrents", arr}};
		changed_keys.clear();
		for (auto it = sent_sig.begin(); it != sent_sig.end();)
			it = handles.count(it->first) ? std::next(it) : sent_sig.erase(it);
		for (auto it = sent_pieces.begin(); it != sent_pieces.end();)
			it = handles.count(it->first) ? std::next(it) : sent_pieces.erase(it);
		full_sent = true;
		return json{{"torrents", arr}, {"present", present}, {"full", full}};
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
			int seeds = -1, leechers = -1, fails = -1, skipfails = -1;
			bool updating = false;
			std::string warning, failure, skipped;
			for (auto const &ep : ae.endpoints)
				for (auto const &ih : ep.info_hashes)
				{
					seeds = std::max(seeds, ih.scrape_complete);
					leechers = std::max(leechers, ih.scrape_incomplete);
					updating = updating || ih.updating;
					if (ih.fails == 0 && !ih.start_sent)
						continue; // not tried yet
					if (ih.fails > 0 && ih.last_error == lt::errors::announce_skipped)
					{
						// A listen socket that cannot reach the tracker's
						// address (loopback, a bridge without a route) skips
						// the announce: that says nothing about the tracker
						if (skipfails < 0 || int(ih.fails) < skipfails)
							skipfails = int(ih.fails);
						if (skipped.empty())
							skipped = ih.last_error.message();
						continue;
					}
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
			if (fails < 0 && skipfails >= 0)
			{
				// No listen socket can reach it at all
				fails = skipfails;
				failure = skipped;
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
			return json{{"dcbt", DCBT_VERSION}, {"libtorrent", LIBTORRENT_VERSION}, {"hardware", tuning_json()}};
		if (cmd == "apply_settings")
		{
			json next = req.value("settings", req);
			// Peer exchange is chosen when the session starts: dcd restarts dc-bt
			if (next.value("pex", true) != settings.value("pex", true))
				throw std::runtime_error("restart required");
			bool was_low = low_memory(settings);
			settings = next;
			ses->apply_settings(pack_from(settings));
			if (low_memory(settings) != was_low)
				dlog("memory profile: %s", low_memory(settings) ? "low" : "normal");
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
			std::string k = key_arg(req);
			auto aj = adopt_jobs.find(k);
			if (aj != adopt_jobs.end())
			{
				aj->second->cancel = true; // the thread stops soon
				adopt_jobs.erase(aj);
			}
			Options o = opts.count(k) ? opts[k] : load_opts(k);
			auto it = handles.find(k);
			if (it != handles.end() && it->second.is_valid())
				ses->remove_torrent(it->second);
			forget(k);
			if (o.untrusted && !o.seed_dir.empty())
			{
				// The folder it seeded from after the hand-over; the user has
				// the data
				std::string perr;
				Fd parent = open_dir(dir_name(o.seed_dir), perr);
				if (parent.ok())
					remove_tree(parent.fd, base_name(o.seed_dir));
			}
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

	// hand_over_more: files of a handed-over torrent chosen later are
	// downloaded into its own folder; once they are complete they are handed
	// over beside the others. What the user has put in their place stays.
	void hand_over_more(std::string const &k, lt::torrent_handle const &h, Options const &o)
	{
		auto ti = h.torrent_file();
		if (!ti || o.handed_to.empty())
			return;
		std::string cur = h.status(lt::torrent_handle::query_save_path).save_path, perr;
		if (!private_dir(cur, perr))
		{
			dlog("%s: files not handed over: %s", k.c_str(), perr.c_str());
			return;
		}
		std::vector<std::string> skipped;
		std::string err = hand_over(cur, o.handed_to, hand_files(h, ti, o.handed_root.empty() ? top_of(ti->files()) : o.handed_root), true,
			o.owner_uid, o.owner_gid, skipped);
		if (!err.empty())
			dlog("%s: files not handed over: %s", k.c_str(), err.c_str());
		if (!skipped.empty())
			dlog("%s: %d files not handed over: something else is in their place", k.c_str(), int(skipped.size()));
	}

	// note_change remembers the torrents an alert is about for the next
	// changed answer of status_all. The statuses that changed (state, progress,
	// a transfer rate above 0 or the last one at 0) come in state_update_alert.
	void note_change(lt::alert *a)
	{
		if (auto *su = lt::alert_cast<lt::state_update_alert>(a))
		{
			for (auto const &st : su->status)
				changed_keys.insert(key_of(st.info_hashes));
			got_state_update = true;
			return;
		}
		auto *ta = dynamic_cast<lt::torrent_alert *>(a);
		if (!ta || !ta->handle.is_valid())
			return;
		for (auto const &kv : handles)
			if (kv.second == ta->handle)
			{
				changed_keys.insert(kv.first);
				return;
			}
	}

	void handle_alerts()
	{
		std::vector<lt::alert *> alerts;
		ses->pop_alerts(&alerts);
		for (lt::alert *a : alerts)
		{
			note_change(a);
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
				auto oi = opts.find(k);
				if (oi != opts.end() && oi->second.untrusted && oi->second.handed)
					hand_over_more(k, f->handle, oi->second);
				event(json{{"event", "torrent_finished"}, {"infohash", k}});
			}
			else if (auto *mv = lt::alert_cast<lt::storage_moved_alert>(a))
			{
				std::string k = key_of(mv->handle.info_hashes());
				auto it = opts.find(k);
				std::string where = mv->storage_path();
				if (it != opts.end())
				{
					it->second.move_error.clear();
					if (it->second.untrusted && !it->second.seed_dir.empty() && where == it->second.seed_dir)
					{
						// Handed over, and seeding from its own folder
						it->second.handed = true;
						save_opts(k);
						where = it->second.handed_to;
					}
				}
				// The new save path must survive a restart right away
				mv->handle.save_resume_data(lt::torrent_handle::save_info_dict);
				event(json{{"event", "storage_moved"}, {"infohash", k}, {"save_path", where}});
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
		resp["id"] = jget(req, "id", 0LL);
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
		while (!g_stop)
		{
			struct pollfd fds[2];
			int n = 0;
			fds[n++] = {listen_fd, POLLIN, 0};
			if (client_fd >= 0)
				fds[n++] = {client_fd, short(POLLIN | (outoff < outbuf.size() ? POLLOUT : 0)), 0};
			int r = ::poll(fds, nfds_t(n), 100);
			if (r > 0)
			{
				if (fds[0].revents & POLLIN)
				{
					int c = ::accept4(listen_fd, nullptr, nullptr, SOCK_CLOEXEC | SOCK_NONBLOCK);
					if (c >= 0)
					{
						drop_client();
						client_fd = c;
						full_sent = false;
					}
				}
				if (n > 1 && client_fd >= 0 && (fds[1].revents & POLLOUT))
					flush_out();
				if (n > 1 && client_fd >= 0 && (fds[1].revents & (POLLIN | POLLHUP | POLLERR)))
				{
					char buf[65536];
					ssize_t got = ::recv(client_fd, buf, sizeof buf, 0);
					if (got == 0 || (got < 0 && errno != EAGAIN && errno != EWOULDBLOCK && errno != EINTR))
						drop_client();
					else if (got > 0)
					{
						inbuf.append(buf, size_t(got));
						if (inbuf.size() > (64u << 20))
							drop_client();
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
			// Nothing that goes wrong with one alert or check ends the engine
			try
			{
				tick();
			}
			catch (std::exception const &e)
			{
				dlog("error: %s", e.what());
			}
			catch (...)
			{
				dlog("error");
			}
		}
		dlog("shutting down");
		try
		{
			save_all(15000);
		}
		catch (std::exception const &e)
		{
			dlog("saving: %s", e.what());
		}
		if (client_fd >= 0)
			::close(client_fd);
		::close(listen_fd);
		::unlink(socket_path.c_str());
		ses->pause();
		ses.reset();
	}

	clk::time_point last_seed = clk::now(), last_save = clk::now(), last_guard = clk::now();

	void tick()
	{
		handle_alerts();
		drop_bad_seeds();
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
};

// trust_ca_file adds the certificate authorities in path (PEM) to those
// HTTPS trackers and web seeds are checked against, beside the system's,
// never in their place. OpenSSL takes one default file (SSL_CERT_FILE, else
// its built-in path): that file and path are written together into a file
// of dc-bt's own, which becomes the default file. libtorrent loads the
// system's usual bundle files and the hashed folder (SSL_CERT_DIR) itself.
static void trust_ca_file(std::string const &path, std::string const &state_dir)
{
	std::vector<char> extra, sys;
	if (!read_file(path, extra) || extra.empty())
	{
		dlog("CA list %s cannot be read: the system's certificate authorities only", path.c_str());
		return;
	}
	char const *env = getenv(X509_get_default_cert_file_env());
	std::string sysfile = env && *env ? env : X509_get_default_cert_file();
	read_file(sysfile, sys);
	std::string merged(sys.begin(), sys.end());
	if (!merged.empty() && merged.back() != '\n')
		merged.push_back('\n');
	merged.append(extra.begin(), extra.end());
	std::string out = state_dir + "/ca.pem";
	if (!write_file(out, merged.data(), merged.size()))
	{
		dlog("cannot write %s: the system's certificate authorities only", out.c_str());
		return;
	}
	setenv(X509_get_default_cert_file_env(), out.c_str(), 1);
	dlog("certificate authorities: the system's and %s", path.c_str());
}

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
	std::string settings_path, ca_file;
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
		else if (a == "--ca-file")
			ca_file = next();
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
		fprintf(stderr, "usage: dc-bt --socket <path> --state <dir> [--torrents <dir>] [--settings <file>] [--ca-file <file>]\n");
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
	if (!ca_file.empty())
		trust_ca_file(ca_file, e.state_dir);
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
	dlog("ready (libtorrent %s), tuned for %s", LIBTORRENT_VERSION, e.tuning_json().dump().c_str());
	e.run();
	return 0;
}
