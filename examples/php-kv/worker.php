<?php

// A PSR-7 worker that keeps its state in kest's kv plugin, so every worker
// of the pool sees the same data, and it outlives worker restarts.

declare(strict_types=1);

require __DIR__ . '/vendor/autoload.php';

use Nyholm\Psr7\Factory\Psr17Factory;
use Nyholm\Psr7\Response;
use Psr\Http\Message\ResponseInterface;
use Psr\Http\Message\ServerRequestInterface;
use Psr\SimpleCache\CacheInterface;
use Spiral\Goridge\RPC\RPC;
use Spiral\RoadRunner\Environment;
use Spiral\RoadRunner\Http\PSR7Worker;
use Spiral\RoadRunner\KeyValue\Factory;
use Spiral\RoadRunner\Worker;

$factory = new Psr17Factory();
$worker = new PSR7Worker(Worker::create(), $factory, $factory, $factory);

// RR_RPC is the address of kest's RPC server; kest sets it for its workers.
$cache = (new Factory(RPC::create(Environment::fromGlobals()->getRPCAddress())))->select('local');

while (true) {
    try {
        $request = $worker->waitRequest();
        if ($request === null) {
            break; // kest asked the worker to stop
        }
    } catch (\Throwable $e) {
        $worker->respond(new Response(400));
        continue;
    }

    try {
        $worker->respond(handle($request, $cache));
    } catch (\Throwable $e) {
        $worker->getWorker()->error((string) $e);
    }
}

function handle(ServerRequestInterface $request, CacheInterface $cache): ResponseInterface
{
    $path = $request->getUri()->getPath();

    if ($path === '/') {
        // Two workers can read the same count at once: a demo, not a counter
        // to rely on.
        $visits = $cache->get('visits', 0) + 1;
        $cache->set('visits', $visits);
        return text(200, sprintf("visits: %d (worker %d)\n", $visits, getmypid()));
    }

    if (preg_match('#^/notes/([\w-]+)$#', $path, $m)) {
        $key = 'note:' . $m[1];
        return match ($request->getMethod()) {
            'GET' => $cache->has($key) ? text(200, $cache->get($key)) : text(404, "no such note\n"),
            'PUT' => $cache->set($key, (string) $request->getBody(), 3600) ? text(204, '') : text(500, ''),
            'DELETE' => $cache->delete($key) ? text(204, '') : text(500, ''),
            default => text(405, "use GET, PUT or DELETE\n"),
        };
    }

    return text(404, "Not found\n");
}

function text(int $status, string $body): ResponseInterface
{
    return new Response($status, ['Content-Type' => 'text/plain; charset=utf-8'], $body);
}
