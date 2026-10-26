<?php

// roadrunner/kv's PSR-16 Cache, unchanged, against kest's kv plugin at RR_RPC.
// Prints one JSON line per check: its name and what it got.

declare(strict_types=1);

require __DIR__ . '/vendor/autoload.php';

use Spiral\Goridge\RPC\RPC;
use Spiral\RoadRunner\KeyValue\Factory;

$factory = new Factory(RPC::create(getenv('RR_RPC')));
$cache = $factory->select('local');

$checks = [
    'set and get' => function () use ($cache) {
        $cache->set('a', ['x' => 1, 'y' => [true, null]]);
        return $cache->get('a');
    },
    'missing' => fn() => $cache->get('missing', 'default'),
    'has' => fn() => [$cache->has('a'), $cache->has('missing')],
    'multiple' => function () use ($cache) {
        $cache->setMultiple(['m1' => 1, 'm2' => 'two']);
        return iterator_to_array($cache->getMultiple(['m1', 'm2', 'm3'], 'none'));
    },
    'delete' => function () use ($cache) {
        $cache->delete('m1');
        $cache->deleteMultiple(['m2', 'never']);
        return [$cache->has('m1'), $cache->has('m2')];
    },
    'ttl' => function () use ($cache) {
        $cache->set('t', 1, 3600);
        $ttl = $cache->getTtl('t');
        return [abs($ttl->getTimestamp() - (time() + 3600)) <= 2, $cache->getTtl('a')];
    },
    'ttl interval' => function () use ($cache) {
        $cache->set('i', 1, new \DateInterval('PT1H'));
        return $cache->getTtl('i') instanceof \DateTimeInterface;
    },
    'zero ttl deletes' => function () use ($cache) {
        $cache->set('z', 1);
        $cache->set('z', 2, 0);
        return $cache->has('z');
    },
    'expires' => function () use ($cache) {
        $cache->set('e', 1, 1);
        sleep(2);
        return $cache->get('e', 'gone');
    },
    'large value' => function () use ($cache) {
        $cache->set('large', str_repeat('x', 100_000));
        return strlen($cache->get('large'));
    },
    'clear' => function () use ($cache) {
        $cache->clear();
        return [$cache->has('a'), $cache->has('t')];
    },
    'other storage' => function () use ($factory, $cache) {
        $factory->select('other')->set('a', 'other');
        $cache->set('a', 'local');
        return [$factory->select('other')->get('a'), $cache->get('a')];
    },
    'no such storage' => fn() => $factory->select('nope')->get('a'),
];
foreach ($checks as $name => $check) {
    try {
        $result = ['got' => $check()];
    } catch (\Throwable $e) {
        $result = ['error' => get_class($e), 'message' => $e->getMessage()];
    }
    echo json_encode(['check' => $name] + $result, JSON_UNESCAPED_SLASHES), "\n";
}
