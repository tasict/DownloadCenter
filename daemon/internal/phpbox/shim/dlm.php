<?php
// Runs a search module packed as .dlm: dcd makes the curl handle, the
// module's prepare($curl, $query[, $username, $password]) sets it up, the
// request is made, and parse($plugin, $response) hands the results to the
// object below. VerifyAccount($username, $password) checks an account.
//
// Written from the interface as documented (method names, arguments); where
// the behaviour is not documented, the choice made is said at the place.

require '/host/common.php';

// DcModuleResults is the $plugin object a module's parse() receives.
final class DcModuleResults
{
    public function addResult($title, $download, $size, $datetime, $page, $hash, $seeds, $leechs, $category)
    {
        return DcBox::add($title, $download, $size, $datetime, $page, $hash, $seeds, $leechs, $category);
    }

    // addJsonResults reads a JSON answer: the list under $resultKey (the
    // whole answer when empty; "a.b" or "a/b" for a nested key, a choice of
    // ours) and, per item, the keys $fieldmapping names for title, download,
    // hash, size, page, date, seeds, leechs and category.
    public function addJsonResults($response, $resultKey, $fieldmapping)
    {
        $data = is_string($response) ? json_decode($response, true) : $response;
        if (is_object($data)) {
            $data = json_decode(json_encode($data), true);
        }
        $list = $this->path($data, (string)$resultKey);
        if (!is_array($list) || !is_array($fieldmapping)) {
            return 0;
        }
        $n = 0;
        foreach ($list as $item) {
            if (is_object($item)) {
                $item = json_decode(json_encode($item), true);
            }
            if (!is_array($item)) {
                continue;
            }
            $f = array();
            foreach (array('title', 'download', 'hash', 'size', 'page', 'date', 'seeds', 'leechs', 'category') as $k) {
                $f[$k] = isset($fieldmapping[$k]) ? $this->path($item, (string)$fieldmapping[$k]) : '';
            }
            if (!DcBox::add($f['title'], $f['download'], $f['size'], $f['date'], $f['page'], $f['hash'], $f['seeds'], $f['leechs'], $f['category'])) {
                break;
            }
            $n++;
        }
        return $n;
    }

    // addRSSResults reads an RSS or Atom answer: title, the enclosure (or
    // link) as the download, its length as the size, pubDate, comments (or
    // the link when the enclosure is the download) as the page, category,
    // and from any other namespace the usual torrent elements (infoHash,
    // contentLength, seeds, peers, magnetURI). The format of this mapping is
    // not documented; these are the common feed fields.
    public function addRSSResults($response)
    {
        if (!is_string($response) || $response === '') {
            return 0;
        }
        $prev = libxml_use_internal_errors(true);
        $xml = simplexml_load_string($response, 'SimpleXMLElement', LIBXML_NONET | LIBXML_NOCDATA);
        libxml_use_internal_errors($prev);
        if ($xml === false) {
            return 0;
        }
        $items = $xml->xpath('//*[local-name()="item" or local-name()="entry"]');
        $n = 0;
        foreach ($items ? $items : array() as $it) {
            $title = (string)$it->title;
            $link = (string)$it->link;
            if ($link === '' && isset($it->link['href'])) {
                $link = (string)$it->link['href'];
            }
            $download = '';
            $size = '';
            if (isset($it->enclosure)) {
                $download = (string)$it->enclosure['url'];
                $size = (string)$it->enclosure['length'];
            }
            $page = (string)$it->comments;
            $date = (string)$it->pubDate;
            if ($date === '') {
                $date = (string)$it->updated;
            }
            $hash = $seeds = $peers = $magnet = '';
            foreach ($it->getNamespaces(true) as $ns) {
                $x = $it->children($ns);
                foreach (array('infoHash' => 'hash', 'contentLength' => 'size', 'seeds' => 'seeds', 'peers' => 'peers', 'magnetURI' => 'magnet') as $name => $var) {
                    if (isset($x->$name) && (string)$x->$name !== '' && $$var === '') {
                        $$var = (string)$x->$name;
                    }
                }
            }
            if ($download === '') {
                $download = $magnet !== '' ? $magnet : $link;
            } elseif ($page === '') {
                $page = $link;
            }
            if (!DcBox::add($title, $download, $size, $date, $page, $hash, $seeds, $peers, (string)$it->category)) {
                break;
            }
            $n++;
        }
        return $n;
    }

    private function path($data, $key)
    {
        if ($key === '') {
            return $data;
        }
        if (is_array($data) && array_key_exists($key, $data)) {
            return $data[$key];
        }
        foreach (preg_split('#[./]#', $key) as $part) {
            if (!is_array($data) || !array_key_exists($part, $data)) {
                return null;
            }
            $data = $data[$part];
        }
        return $data;
    }
}

$req = DcBox::request();
try {
    $class = DcBox::load();
    $module = new $class();
    $user = DcBox::get('username');
    $pass = DcBox::get('password');
    if (DcBox::get('mode') === 'verify') {
        if (!method_exists($module, 'VerifyAccount')) {
            DcBox::finish('the module cannot check accounts');
        }
        DcBox::$verified = (bool)$module->VerifyAccount($user, $pass);
        DcBox::finish();
    }
    $curl = DcBox::curl(curl_init());
    if (!empty($req['account'])) {
        $module->prepare($curl, DcBox::get('keyword'), $user, $pass);
    } else {
        $module->prepare($curl, DcBox::get('keyword'));
    }
    // The answer is needed as a string without the headers, whatever
    // prepare set
    curl_setopt($curl, CURLOPT_RETURNTRANSFER, true);
    curl_setopt($curl, CURLOPT_HEADER, false);
    $response = curl_exec($curl);
    if ($response === false) {
        DcBox::finish('request failed: ' . curl_error($curl));
    }
    curl_close($curl);
    $module->parse(new DcModuleResults(), $response);
    DcBox::finish();
} catch (Throwable $e) {
    DcBox::finish('the plugin failed: ' . $e->getMessage());
}
