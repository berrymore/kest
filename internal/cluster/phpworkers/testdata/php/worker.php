<?php

// An unmodified roadrunner/http PSR-7 worker for kest's integration tests
// (internal/cluster/phpworkers/php_integration_test.go). Routes:
//
//   /echo           the request as PHP sees it, as JSON
//   /status/<code>  an empty response with that status
//   /headers        multi-value headers and cookies
//   /big/<bytes>    a body of that many bytes
//   /sleep/<ms>     sleep, then answer "slept"
//   /error          a soft error ($worker->error())
//   /exit           the process exits mid-request
//   /fatal          a PHP fatal error
//   /stream/<n>     n chunks from a generator, 50 ms apart (HttpWorker stream
//                   mode); a stopped stream is logged to stderr
//   /hints          103 Early Hints, then the response
//   /chunked/<n>    a PSR-7 body of n bytes sent in 1000-byte chunks
//
// If the file named by KEST_TEST_BOOT_MARKER exists, the worker fails to boot,
// like a deploy of code that doesn't start.

declare(strict_types=1);

require __DIR__ . '/vendor/autoload.php';

$marker = getenv('KEST_TEST_BOOT_MARKER');
if ($marker !== false && file_exists($marker)) {
    fwrite(STDERR, "boot refused: $marker exists\n");
    exit(1);
}

use Nyholm\Psr7\Factory\Psr17Factory;
use Nyholm\Psr7\Response;
use Psr\Http\Message\ServerRequestInterface;
use Psr\Http\Message\UploadedFileInterface;
use Spiral\RoadRunner\Http\PSR7Worker;
use Spiral\RoadRunner\Worker;

$factory = new Psr17Factory();
$worker = new PSR7Worker(Worker::create(), $factory, $factory, $factory);

while (true) {
    try {
        $request = $worker->waitRequest();
        if ($request === null) {
            break;
        }
    } catch (\Throwable $e) {
        $worker->respond(new Response(400, [], 'bad request: ' . $e->getMessage()));
        continue;
    }

    $parts = explode('/', trim($request->getUri()->getPath(), '/'));
    $arg = $parts[1] ?? '';
    switch ($parts[0]) {
        case 'echo':
            $worker->respond(new Response(200, ['Content-Type' => 'application/json'], describe($request)));
            break;
        case 'status':
            $worker->respond(new Response((int) $arg));
            break;
        case 'headers':
            $worker->respond(new Response(200, [
                'Content-Type' => 'text/plain',
                'X-Multi' => ['one', 'two'],
                'Set-Cookie' => ['a=1; Path=/', 'b=2; Path=/; HttpOnly'],
            ], 'headers'));
            break;
        case 'big':
            $worker->respond(new Response(200, [], str_repeat('x', (int) $arg)));
            break;
        case 'sleep':
            usleep((int) $arg * 1000);
            $worker->respond(new Response(200, [], 'slept'));
            break;
        case 'error':
            $worker->getWorker()->error('something broke');
            break;
        case 'exit':
            exit(3);
        case 'fatal':
            undefined_function();
            break;
        case 'stream':
            $worker->getHttpWorker()->respond(200, chunks((int) $arg), ['Content-Type' => 'text/plain']);
            break;
        case 'hints':
            $worker->getHttpWorker()->respond(103, '', ['Link' => '</app.css>; rel=preload'], false);
            $worker->respond(new Response(200, ['Content-Type' => 'text/plain'], 'after hints'));
            break;
        case 'chunked':
            $chunked = new PSR7Worker($worker->getWorker(), $factory, $factory, $factory);
            $chunked->chunkSize = 1000;
            $chunked->respond(new Response(200, [], str_repeat('y', (int) $arg)));
            break;
        default:
            $worker->respond(new Response(404, [], 'no route'));
    }
}

function chunks(int $n): \Generator
{
    try {
        for ($i = 0; $i < $n; $i++) {
            yield "chunk $i\n";
            usleep(50000);
        }
    } catch (\Spiral\RoadRunner\Http\Exception\StreamStoppedException $e) {
        fwrite(STDERR, "stream stopped\n");
        throw $e;
    }
}

function describe(ServerRequestInterface $request): string
{
    $files = [];
    flattenFiles($request->getUploadedFiles(), '', $files);

    return json_encode([
        'method' => $request->getMethod(),
        'uri' => (string) $request->getUri(),
        'protocol' => $request->getProtocolVersion(),
        'headers' => (object) $request->getHeaders(),
        'cookies' => (object) $request->getCookieParams(),
        'query' => (object) $request->getQueryParams(),
        'parsed' => $request->getParsedBody(),
        'body' => (string) $request->getBody(),
        'attributes' => $request->getAttributes(),
        'files' => (object) $files,
        'server' => array_intersect_key($_SERVER, array_flip([
            'REMOTE_ADDR', 'REQUEST_METHOD', 'REQUEST_URI', 'HTTP_HOST', 'CONTENT_TYPE', 'RR_MODE', 'RR_RELAY', 'KEST_TEST',
        ])),
    ], JSON_THROW_ON_ERROR | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE);
}

/** @param array<array-key, UploadedFileInterface|array> $files */
function flattenFiles(array $files, string $prefix, array &$out): void
{
    foreach ($files as $key => $file) {
        $name = $prefix === '' ? (string) $key : $prefix . '[' . $key . ']';
        if (is_array($file)) {
            flattenFiles($file, $name, $out);
            continue;
        }
        $out[$name] = [
            'name' => $file->getClientFilename(),
            'type' => $file->getClientMediaType(),
            'size' => $file->getSize(),
            'error' => $file->getError(),
            'content' => $file->getError() === UPLOAD_ERR_OK ? (string) $file->getStream() : null,
        ];
    }
}
