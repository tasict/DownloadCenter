<?php
// The self-check dcd runs before it lets plugins run: what the box looks
// like from inside. It runs without open_basedir so that it sees what the
// kernel lets through, then tightens open_basedir and checks that it holds.
// dcd decides from the answer.

require '/host/common.php';

$req = DcBox::request();
$r = array();
$r['php'] = PHP_VERSION;
$r['uid'] = posix_getuid();
$r['euid'] = posix_geteuid();
$r['gid'] = posix_getgid();
$r['groups'] = posix_getgroups();
$root = @scandir('/');
$r['root'] = $root === false ? null : array_values(array_diff($root, array('.', '..')));
$r['exists'] = array();
foreach ($req['paths'] as $p) {
    $r['exists'][$p] = @file_exists($p);
}
$r['writable'] = array('/' => @is_writable('/'), '/lib' => @is_writable('/lib'), '/tmp' => @is_writable('/tmp'),
    '/plugin' => @is_writable('/plugin'), '/host' => @is_writable('/host'));
$tmp = @file_put_contents('/tmp/probe', 'x');
$r['tmp_write'] = $tmp === 1;
$procs = array();
foreach ((array)@scandir('/proc') as $e) {
    if (ctype_digit((string)$e)) {
        $procs[] = (int)$e;
    }
}
$r['procs'] = $procs;
$r['functions'] = array();
foreach (array('exec', 'shell_exec', 'system', 'passthru', 'proc_open', 'popen', 'pcntl_exec', 'mail', 'putenv',
    'fsockopen', 'stream_socket_client', 'socket_create', 'dl', 'posix_setuid', 'posix_kill') as $f) {
    $r['functions'][$f] = function_exists($f);
}
$r['extensions'] = array();
foreach (array('curl', 'json', 'mbstring', 'iconv', 'openssl', 'zlib', 'posix', 'simplexml') as $e) {
    $r['extensions'][$e] = extension_loaded($e);
}

// Without the proxy: nothing but the relay can be reached
$ch = curl_init('http://' . $req['direct'] . '/');
curl_setopt($ch, CURLOPT_PROXY, '');
curl_setopt($ch, CURLOPT_NOPROXY, '*');
curl_setopt($ch, CURLOPT_CONNECTTIMEOUT, 3);
curl_setopt($ch, CURLOPT_TIMEOUT, 5);
curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
$r['direct'] = curl_exec($ch) !== false;
curl_close($ch);

// Through the relay, plain and tunnelled
$ch = DcBox::curl(curl_init('http://probe.invalid/'));
$r['relay_http'] = (string)curl_exec($ch);
curl_close($ch);
$ch = DcBox::curl(curl_init('https://probe.invalid/'));
curl_setopt($ch, CURLOPT_TIMEOUT, 5);
curl_exec($ch);
curl_close($ch);

// The kernel's limits on this process: the syscall filter (mode 2), which
// refuses even a FIFO, and the memory limit
$r['seccomp'] = null;
foreach ((array)@file('/proc/self/status') as $l) {
    if (preg_match('/^Seccomp:\s+(\d+)/', $l, $m)) {
        $r['seccomp'] = (int)$m[1];
    }
}
$r['fifo_made'] = @posix_mkfifo('/tmp/probe.fifo', 0600);
$r['fifo_errno'] = posix_get_last_error();
$lim = posix_getrlimit();
$r['address_space'] = (string)$lim['soft totalmem'];
$r['max_processes'] = (string)$lim['soft maxproc'];

// open_basedir: it can be tightened, never loosened
ini_set('open_basedir', '/plugin:/host:/tmp');
$r['basedir_root'] = @scandir('/') !== false;
$r['basedir_loosen'] = @ini_set('open_basedir', '/') !== false && ini_get('open_basedir') === '/';

DcBox::$probe = $r;
DcBox::finish();
