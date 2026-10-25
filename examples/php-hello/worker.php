<?php

// A PSR-7 worker: it boots once, then serves requests in a loop. kest starts
// several of these processes and sends each request to a free one.

declare(strict_types=1);

require __DIR__ . '/vendor/autoload.php';

use Nyholm\Psr7\Factory\Psr17Factory;
use Nyholm\Psr7\Response;
use Psr\Http\Message\ResponseInterface;
use Psr\Http\Message\ServerRequestInterface;
use Spiral\RoadRunner\Http\PSR7Worker;
use Spiral\RoadRunner\Worker;

$factory = new Psr17Factory();
$worker = new PSR7Worker(Worker::create(), $factory, $factory, $factory);

// Anything expensive (container, config, connections) goes here: it runs once
// per worker, not once per request.
$bootedAt = date(DATE_ATOM);

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
        $worker->respond(handle($request, $bootedAt));
    } catch (\Throwable $e) {
        // Reported to kest, which logs it and answers 500; the worker keeps serving.
        $worker->getWorker()->error((string) $e);
    }
}

function handle(ServerRequestInterface $request, string $bootedAt): ResponseInterface
{
    $path = $request->getUri()->getPath();

    if ($path === '/') {
        $name = $request->getQueryParams()['name'] ?? 'world';
        return text(200, sprintf("Hello, %s! (PHP %s, worker %d)\n", $name, PHP_VERSION, getmypid()));
    }

    if ($path === '/info') {
        return new Response(200, ['Content-Type' => 'application/json'], json_encode([
            'worker_pid' => getmypid(),
            'request_id' => $request->getHeaderLine('X-Request-Id'),
            'booted_at' => $bootedAt,
            'method' => $request->getMethod(),
            'protocol' => $request->getProtocolVersion(),
            'uri' => (string) $request->getUri(),
            'client' => $request->getServerParams()['REMOTE_ADDR'] ?? null,
            'parsed_body' => $request->getParsedBody(),
            'memory' => memory_get_usage(true),
        ], JSON_THROW_ON_ERROR | JSON_UNESCAPED_SLASHES) . "\n");
    }

    if ($path === '/fail') {
        throw new RuntimeException('this route always fails');
    }

    return text(404, "Not found\n");
}

function text(int $status, string $body): ResponseInterface
{
    return new Response($status, ['Content-Type' => 'text/plain; charset=utf-8'], $body);
}
