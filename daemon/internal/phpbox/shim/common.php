<?php
// Shared by the runners: reads the request dcd sends on stdin, collects the
// results and writes the answer as JSON to file descriptor 3. Whatever a
// plugin prints goes to stdout, which dcd throws away.
//
// Download Center's own code (MIT). It implements interfaces that search
// plugins for other download managers expect; it contains no code of those
// products and is not affiliated with their makers.

final class DcBox
{
    const MAX_RESULTS = 500;
    const MAX_TEXT = 4096;

    public static $request = array();
    public static $results = array();
    public static $verified = null;
    public static $probe = null;
    private static $done = false;

    // request reads the request: a JSON object on stdin.
    public static function request()
    {
        $raw = stream_get_contents(STDIN, 1 << 20);
        $req = json_decode((string)$raw, true);
        if (!is_array($req)) {
            self::finish('bad request');
        }
        self::$request = $req;
        register_shutdown_function(array('DcBox', 'shutdown'));
        return $req;
    }

    // get is a field of the request as a string ('' when missing).
    public static function get($key)
    {
        $v = isset(self::$request[$key]) ? self::$request[$key] : '';
        return is_scalar($v) ? (string)$v : '';
    }

    // text turns a value into a string of at most MAX_TEXT bytes; anything
    // that is not a string or a number becomes ''.
    public static function text($v)
    {
        if ($v instanceof DateTimeInterface) {
            return $v->format(DATE_ATOM);
        }
        if (is_bool($v) || !is_scalar($v)) {
            return '';
        }
        $s = trim((string)$v);
        return strlen($s) > self::MAX_TEXT ? substr($s, 0, self::MAX_TEXT) : $s;
    }

    // number turns a value into a number; -1 when it is not one. Sizes may
    // come as "1.2 GB".
    public static function number($v)
    {
        if (is_int($v) || is_float($v)) {
            return $v >= 0 ? $v : -1;
        }
        if (!is_string($v)) {
            return -1;
        }
        $s = trim(str_replace(',', '', $v));
        if (is_numeric($s)) {
            return $s + 0 >= 0 ? $s + 0 : -1;
        }
        if (preg_match('/^([0-9.]+)\s*([KMGTP]?)i?B?$/i', $s, $m)) {
            $units = array('' => 1, 'K' => 1024, 'M' => 1048576, 'G' => 1073741824, 'T' => 1099511627776, 'P' => 1125899906842624);
            return (float)$m[1] * $units[strtoupper($m[2])];
        }
        return -1;
    }

    // add keeps one result; false once there are MAX_RESULTS.
    public static function add($title, $download, $size, $date, $page, $hash, $seeds, $leechs, $category)
    {
        if (count(self::$results) >= self::MAX_RESULTS) {
            return false;
        }
        self::$results[] = array(
            'title' => self::text($title),
            'download' => self::text($download),
            'size' => self::number($size),
            'date' => self::text($date),
            'page' => self::text($page),
            'hash' => self::text($hash),
            'seeds' => self::number($seeds),
            'leechs' => self::number($leechs),
            'category' => self::text($category),
        );
        return true;
    }

    // curl sets the defaults of a handle dcd makes for a plugin: our
    // User-Agent, time limits, redirects, http and https only, and TLS
    // certificates checked.
    public static function curl($ch)
    {
        curl_setopt($ch, CURLOPT_USERAGENT, self::get('user_agent'));
        curl_setopt($ch, CURLOPT_CONNECTTIMEOUT, 10);
        curl_setopt($ch, CURLOPT_TIMEOUT, 20);
        curl_setopt($ch, CURLOPT_FOLLOWLOCATION, true);
        curl_setopt($ch, CURLOPT_MAXREDIRS, 5);
        curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
        curl_setopt($ch, CURLOPT_ENCODING, '');
        curl_setopt($ch, CURLOPT_SSL_VERIFYPEER, true);
        curl_setopt($ch, CURLOPT_SSL_VERIFYHOST, 2);
        if (defined('CURLOPT_PROTOCOLS')) {
            curl_setopt($ch, CURLOPT_PROTOCOLS, CURLPROTO_HTTP | CURLPROTO_HTTPS);
            curl_setopt($ch, CURLOPT_REDIR_PROTOCOLS, CURLPROTO_HTTP | CURLPROTO_HTTPS);
        }
        // The system's certificate authorities and the package's list
        $capath = self::get('capath');
        if ($capath !== '') {
            curl_setopt($ch, CURLOPT_CAPATH, $capath);
        }
        $cainfo = self::get('cainfo');
        if ($cainfo !== '') {
            curl_setopt($ch, CURLOPT_CAINFO, $cainfo);
        }
        return $ch;
    }

    // finish writes the answer and ends the run.
    public static function finish($error = null)
    {
        if (self::$done) {
            return;
        }
        self::$done = true;
        $flags = 0;
        if (defined('JSON_INVALID_UTF8_SUBSTITUTE')) {
            $flags |= JSON_INVALID_UTF8_SUBSTITUTE;
        }
        if (defined('JSON_PARTIAL_OUTPUT_ON_ERROR')) {
            $flags |= JSON_PARTIAL_OUTPUT_ON_ERROR;
        }
        $answer = array('ok' => $error === null, 'results' => self::$results);
        if (self::$verified !== null) {
            $answer['verified'] = (bool)self::$verified;
        }
        if ($error !== null) {
            $answer['error'] = self::text($error);
        }
        if (self::$probe !== null) {
            $answer['probe'] = self::$probe;
        }
        $out = @fopen('php://fd/3', 'wb');
        if ($out) {
            fwrite($out, json_encode($answer, $flags));
            fclose($out);
        }
        exit(0);
    }

    // shutdown reports a run that ended without finish (a fatal error, an
    // exit in the plugin).
    public static function shutdown()
    {
        if (self::$done) {
            return;
        }
        $e = error_get_last();
        $msg = 'the plugin stopped';
        if ($e && in_array($e['type'], array(E_ERROR, E_PARSE, E_CORE_ERROR, E_COMPILE_ERROR), true)) {
            $msg = 'the plugin failed: ' . $e['message'];
        }
        self::finish($msg);
    }

    // load reads the plugin's file, which dcd checked to be in /plugin.
    public static function load()
    {
        $entry = self::get('entry');
        if ($entry === '' || strpos($entry, '..') !== false || $entry[0] === '/') {
            self::finish('bad entry');
        }
        require_once '/plugin/' . $entry;
        $class = self::get('class');
        if (!class_exists($class)) {
            self::finish('the plugin has no class ' . $class);
        }
        return $class;
    }
}
