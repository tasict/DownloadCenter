<?php
// Runs an add-on: its class is made with ($url, $username, $password,
// $meta), Verify() is asked first when it declares IVerify and has an
// account, then Search($keyword, $limit, $category) returns SearchLink
// objects. Only the search and account interfaces are used.
//
// Written from the interface as documented (names, arguments, fields);
// where the behaviour is not documented, the choice made is said at the
// place.

require '/host/common.php';

// The interfaces are declared without methods: an add-on's own method
// signatures then load whatever defaults or types they use, and dcd checks
// for the methods it calls.
interface ISite
{
}

interface ISearch
{
}

interface IVerify
{
}

interface IRss
{
}

interface IDownload
{
}

interface IPostProcess
{
}

// SearchLink is one result of Search(). time is null or a DateTime. Size,
// seeds and peers an add-on leaves alone stay null: unknown, not zero.
class SearchLink
{
    public $src = '';
    public $link = '';
    public $name = '';
    public $size = null;
    public $time = null;
    public $seeds = null;
    public $peers = null;
    public $category = '';
    public $enclosure_url = '';
}

// Ajax makes HTTP requests for an add-on with curl. Options: url, body (a
// raw body), post (fields to post), params (added to the query string),
// cookie (a string or name => value; cookies the site sets are kept for
// the next requests of this object), follow (follow redirects, default
// true), header (list of "Name: value" or name => value), timeout
// (seconds, at most 20). The callback gets ($request, $header, $cookie,
// $body, $effective_url) and its value is returned; without one the body
// is. The method names are not documented: request(), get() and post()
// take the address and the options in either order.
class Ajax
{
    private $cookies = array();
    private $defaults = array();

    public function __construct($options = array())
    {
        if (is_array($options)) {
            $this->defaults = $options;
        }
    }

    public function get()
    {
        return $this->call(func_get_args(), null);
    }

    public function post()
    {
        return $this->call(func_get_args(), 'post');
    }

    public function request()
    {
        return $this->call(func_get_args(), null);
    }

    private function call($args, $method)
    {
        $opt = $this->defaults;
        $callback = null;
        foreach ($args as $a) {
            if (is_string($a) && !isset($opt['url']) && preg_match('#^https?://#i', $a)) {
                $opt['url'] = $a;
            } elseif (is_callable($a) && !is_string($a)) {
                $callback = $a;
            } elseif (is_array($a)) {
                $opt = array_merge($opt, $a);
            } elseif (is_string($a) && $method === 'post' && !isset($opt['body'])) {
                $opt['body'] = $a;
            }
        }
        if ($method === 'post' && !isset($opt['post']) && !isset($opt['body'])) {
            $opt['post'] = array();
        }
        return $this->send($opt, $callback);
    }

    private function send($opt, $callback)
    {
        $url = isset($opt['url']) ? (string)$opt['url'] : '';
        if (!empty($opt['params']) && is_array($opt['params'])) {
            $url .= (strpos($url, '?') === false ? '?' : '&') . http_build_query($opt['params']);
        }
        $ch = DcBox::curl(curl_init());
        curl_setopt($ch, CURLOPT_URL, $url);
        curl_setopt($ch, CURLOPT_HEADER, true);
        curl_setopt($ch, CURLOPT_FOLLOWLOCATION, !isset($opt['follow']) || (bool)$opt['follow']);
        if (isset($opt['timeout']) && is_numeric($opt['timeout'])) {
            curl_setopt($ch, CURLOPT_TIMEOUT, max(1, min(20, (int)$opt['timeout'])));
        }
        if (isset($opt['body'])) {
            curl_setopt($ch, CURLOPT_POST, true);
            curl_setopt($ch, CURLOPT_POSTFIELDS, (string)$opt['body']);
        } elseif (isset($opt['post'])) {
            curl_setopt($ch, CURLOPT_POST, true);
            curl_setopt($ch, CURLOPT_POSTFIELDS, is_array($opt['post']) ? http_build_query($opt['post']) : (string)$opt['post']);
        }
        $headers = array();
        if (!empty($opt['header']) && is_array($opt['header'])) {
            foreach ($opt['header'] as $k => $v) {
                $headers[] = is_int($k) ? (string)$v : $k . ': ' . $v;
            }
        }
        $cookies = $this->cookies;
        if (isset($opt['cookie'])) {
            if (is_array($opt['cookie'])) {
                $cookies = array_merge($cookies, $opt['cookie']);
            } else {
                foreach (explode(';', (string)$opt['cookie']) as $pair) {
                    $kv = explode('=', $pair, 2);
                    if (count($kv) === 2) {
                        $cookies[trim($kv[0])] = trim($kv[1]);
                    }
                }
            }
        }
        if ($cookies) {
            $pairs = array();
            foreach ($cookies as $k => $v) {
                $pairs[] = $k . '=' . $v;
            }
            curl_setopt($ch, CURLOPT_COOKIE, implode('; ', $pairs));
        }
        if ($headers) {
            curl_setopt($ch, CURLOPT_HTTPHEADER, $headers);
        }
        $raw = curl_exec($ch);
        $header = array();
        $body = '';
        if ($raw !== false) {
            $size = (int)curl_getinfo($ch, CURLINFO_HEADER_SIZE);
            $head = substr($raw, 0, $size);
            $body = (string)substr($raw, $size);
            // The headers of the last answer (after redirects)
            $blocks = preg_split("/\r?\n\r?\n/", trim($head));
            foreach (preg_split("/\r?\n/", (string)end($blocks)) as $line) {
                $kv = explode(':', $line, 2);
                if (count($kv) !== 2) {
                    continue;
                }
                $name = trim($kv[0]);
                $value = trim($kv[1]);
                $header[$name] = $value;
                if (strcasecmp($name, 'Set-Cookie') === 0) {
                    $c = explode('=', explode(';', $value, 2)[0], 2);
                    if (count($c) === 2) {
                        $this->cookies[trim($c[0])] = trim($c[1]);
                    }
                }
            }
        }
        $effective = (string)curl_getinfo($ch, CURLINFO_EFFECTIVE_URL);
        curl_close($ch);
        if ($callback !== null) {
            return call_user_func($callback, $opt, $header, $this->cookies, $body, $effective);
        }
        return $raw === false ? false : $body;
    }
}

$req = DcBox::request();
try {
    $class = DcBox::load();
    $user = DcBox::get('username');
    $pass = DcBox::get('password');
    $site = new $class(null, $user === '' ? null : $user, $pass === '' ? null : $pass, null);
    $verify = $site instanceof IVerify && method_exists($site, 'Verify');
    if (DcBox::get('mode') === 'verify') {
        if (!$verify) {
            DcBox::finish('the add-on cannot check accounts');
        }
        DcBox::$verified = (bool)$site->Verify();
        DcBox::finish();
    }
    if ($verify && $user !== '') {
        DcBox::$verified = (bool)$site->Verify();
        if (!DcBox::$verified) {
            DcBox::finish('the account was refused');
        }
    }
    if (!method_exists($site, 'Search')) {
        DcBox::finish('the add-on cannot search');
    }
    // The category is ours (all, video, audio, software, books, other):
    // the add-ons' own category names are not documented.
    $links = $site->Search(DcBox::get('keyword'), (int)DcBox::get('limit'), DcBox::get('category'));
    foreach (is_array($links) ? $links : array() as $l) {
        if (!is_object($l)) {
            continue;
        }
        // link is the download (a magnet link or a .torrent address), the
        // enclosure the fallback; src is taken as the page.
        $download = isset($l->link) ? DcBox::text($l->link) : '';
        if ($download === '' && isset($l->enclosure_url)) {
            $download = DcBox::text($l->enclosure_url);
        }
        $time = isset($l->time) ? $l->time : null;
        if (!DcBox::add(isset($l->name) ? $l->name : '', $download, isset($l->size) ? $l->size : -1,
            $time instanceof DateTimeInterface ? $time : '', isset($l->src) ? $l->src : '', '',
            isset($l->seeds) ? $l->seeds : -1, isset($l->peers) ? $l->peers : -1, isset($l->category) ? $l->category : '')) {
            break;
        }
    }
    DcBox::finish();
} catch (Throwable $e) {
    DcBox::finish('the plugin failed: ' . $e->getMessage());
}
