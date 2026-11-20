package phpbox

// Two small plugins of our own, one per format, used by the sandbox tests.
// They talk to search.test, which the tests' relay hook answers.

const testModule = `<?php
// A search module for tests: prepare with an account, JSON and RSS
// results, a direct result, a request of its own, an account check.
class DcTestSearch
{
    private $q = '';

    public function prepare($curl, $query, $username = '', $password = '')
    {
        $this->q = $query;
        curl_setopt($curl, CURLOPT_URL, 'http://search.test/json?q=' . urlencode($query) . '&u=' . urlencode($username));
    }

    public function parse($plugin, $response)
    {
        $n = $plugin->addJsonResults($response, 'data.items', array('title' => 'name', 'download' => 'magnet',
            'size' => 'bytes', 'seeds' => 'seeders', 'leechs' => 'leechers', 'date' => 'added', 'page' => 'url',
            'hash' => 'ih', 'category' => 'cat'));
        $plugin->addResult('Direct result', 'http://search.test/files/a.torrent', '1.5 MB', '2026-01-02 03:04:05',
            'http://search.test/a', '', 3, 4, 'Software');
        $ch = curl_init('http://search.test/rss?q=' . urlencode($this->q));
        curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
        $n += $plugin->addRSSResults(curl_exec($ch));
        echo str_repeat('noise that must not reach the answer ', 200000);
        return $n + 1;
    }

    public function VerifyAccount($username, $password)
    {
        $ch = curl_init('http://search.test/login');
        curl_setopt($ch, CURLOPT_POST, true);
        curl_setopt($ch, CURLOPT_POSTFIELDS, http_build_query(array('u' => $username, 'p' => $password)));
        curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
        return trim((string)curl_exec($ch)) === 'ok';
    }
}
`

const testAddon = `<?php
// An add-on for tests: Ajax with a callback and without, SearchLink with a
// DateTime, an account check. It also declares IDownload, never called.
class DcTestAddon implements ISite, ISearch, IVerify, IDownload
{
    private $user;
    private $pass;

    public function __construct($url = null, $username = null, $password = null, $meta = null)
    {
        $this->user = $username;
        $this->pass = $password;
    }

    public function Verify()
    {
        $ajax = new Ajax();
        return $ajax->request(array('url' => 'http://search.test/login', 'post' => array('u' => $this->user, 'p' => $this->pass)),
            function ($request, $header, $cookie, $body, $url) {
                return trim($body) === 'ok' && isset($cookie['session']);
            });
    }

    public function Search($keyword, $limit, $category)
    {
        $ajax = new Ajax();
        $body = $ajax->get('http://search.test/addon', array('params' => array('q' => $keyword, 'limit' => $limit, 'cat' => $category)));
        $out = array();
        foreach (json_decode($body, true) as $row) {
            $l = new SearchLink();
            $l->name = $row['name'];
            $l->link = $row['link'];
            $l->src = $row['page'];
            $l->size = $row['size'];
            $l->time = new DateTime($row['time']);
            $l->seeds = $row['seeds'];
            $l->peers = $row['peers'];
            $l->category = $row['cat'];
            $out[] = $l;
        }
        return $out;
    }

    public function Download()
    {
        throw new Exception('the download interface was called');
    }
}
`

// testEscape tries to get out of the box and reports what it saw as the
// title of its one result. targets.json lists the addresses to try.
const testEscape = `<?php
class DcEscape
{
    public function prepare($curl, $query)
    {
        curl_setopt($curl, CURLOPT_URL, 'http://search.test/json?q=x');
    }

    public function parse($plugin, $response)
    {
        $o = array();
        $o['shadow'] = @file_get_contents('/etc/config/shadow');
        $o['share'] = @scandir('/share');
        $o['passwd'] = @file_exists('/etc/passwd');
        $o['outside'] = @file_get_contents('/etc/ssl/certs/ACCVRAIZ1.pem') !== false;
        $o['write'] = @file_put_contents('/plugin/x', 'x') !== false;
        foreach (array('exec', 'shell_exec', 'system', 'passthru', 'proc_open', 'popen', 'fsockopen', 'putenv', 'mail', 'dl') as $f) {
            try {
                $f('id');
                $o['call'][$f] = 'ran';
            } catch (Throwable $e) {
                $o['call'][$f] = get_class($e);
            }
        }
        $o['basedir'] = @ini_set('open_basedir', '/') !== false;
        $targets = json_decode(file_get_contents('/plugin/targets.json'), true);
        foreach ($targets['relay'] as $k => $url) {
            $ch = curl_init($url);
            curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
            curl_setopt($ch, CURLOPT_CONNECTTIMEOUT, 5);
            curl_setopt($ch, CURLOPT_TIMEOUT, 5);
            if (strpos($k, 'tunnel') === 0) {
                curl_setopt($ch, CURLOPT_HTTPPROXYTUNNEL, true);
            }
            $b = curl_exec($ch);
            $o['net'][$k] = array(curl_errno($ch), curl_getinfo($ch, CURLINFO_HTTP_CODE), $b === false ? '' : substr($b, 0, 40));
            curl_close($ch);
        }
        foreach ($targets['direct'] as $k => $url) {
            $ch = curl_init($url);
            curl_setopt($ch, CURLOPT_PROXY, '');
            curl_setopt($ch, CURLOPT_NOPROXY, '*');
            curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
            curl_setopt($ch, CURLOPT_CONNECTTIMEOUT, 3);
            $b = curl_exec($ch);
            $o['direct'][$k] = $b !== false;
            curl_close($ch);
        }
        $plugin->addResult(json_encode($o), 'magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567', 0, '', '', '', 0, 0, '');
        return 1;
    }
}
`

// testLoop never ends; testFlood writes past the answer's limit.
const testLoop = `<?php
class DcLoop
{
    public function prepare($curl, $query)
    {
        while (true) {
        }
    }

    public function parse($plugin, $response)
    {
    }
}
`

const testFlood = `<?php
class DcFlood
{
    public function prepare($curl, $query)
    {
        $f = fopen('php://fd/3', 'wb');
        for ($i = 0; $i < 3; $i++) {
            fwrite($f, str_repeat('{', 1 << 20));
        }
        fclose($f);
        exit(0);
    }

    public function parse($plugin, $response)
    {
    }
}
`
