<?php

// Calls kest's RPC server through roadrunner/goridge, unchanged, at RR_RPC,
// and prints one JSON line per call: the answer, or the exception.

declare(strict_types=1);

require __DIR__ . '/vendor/autoload.php';

use Spiral\Goridge\RPC\Codec\RawCodec;
use Spiral\Goridge\RPC\MultiRPC;
use Spiral\Goridge\RPC\RPC;

$address = getenv('RR_RPC');
$rpc = RPC::create($address);
$test = $rpc->withServicePrefix('test');

$calls = [
    'json' => fn() => $rpc->call('test.Echo', ['a' => 1, 'b' => [true, null]]),
    'prefix' => fn() => $test->call('echo', 'lower-case method'),
    'raw' => fn() => $test->withCodec(new RawCodec())->call('Raw', "\x00bytes"),
    'error' => fn() => $test->call('Fail', 'x'),
    'unknown method' => fn() => $test->call('Nope', null),
    'unknown service' => fn() => $rpc->call('nope.Echo', null),
    'after errors' => fn() => $test->call('Echo', 'still here'),
    'multi' => fn() => MultiRPC::create($address, 2)->withServicePrefix('test')->call('Echo', 'multi'),
];
foreach ($calls as $name => $call) {
    try {
        $result = ['ok' => $call()];
    } catch (\Throwable $e) {
        $result = ['error' => \get_class($e), 'message' => $e->getMessage()];
    }
    if (\is_string($result['ok'] ?? null)) {
        $result['ok'] = \bin2hex($result['ok']);
    }
    echo \json_encode(['call' => $name] + $result, \JSON_UNESCAPED_SLASHES), "\n";
}
