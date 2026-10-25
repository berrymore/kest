<?php

// What a codec costs PHP per request (docs/benchmarks.md): PSR7Worker reads a
// request head with ten headers and answers it, against a fake worker, so no
// I/O. test/bench/run.sh runs it; by hand, from the repository root:
//
//   php test/bench/codec.php json|proto [requests]

declare(strict_types=1);

require __DIR__ . '/../../examples/php-hello/vendor/autoload.php';

use Nyholm\Psr7\Factory\Psr17Factory;
use Nyholm\Psr7\Response;
use RoadRunner\HTTP\DTO\V1\HeaderValue;
use RoadRunner\HTTP\DTO\V1\Request as RequestProto;
use Spiral\RoadRunner\Http\PSR7Worker;
use Spiral\RoadRunner\Payload;
use Spiral\RoadRunner\WorkerInterface;

$codec = $argv[1] ?? '';
$requests = (int) ($argv[2] ?? 50000);

$headers = [
    'Accept' => ['text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8'],
    'Accept-Encoding' => ['gzip, deflate, br'],
    'Accept-Language' => ['en-US,en;q=0.9'],
    'Cache-Control' => ['no-cache'],
    'Connection' => ['keep-alive'],
    'Cookie' => ['session=abc123; theme=dark'],
    'User-Agent' => ['Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko)'],
    'X-Forwarded-For' => ['10.0.0.1'],
    'X-Forwarded-Proto' => ['https'],
    'X-Request-Id' => ['0d6c3c5e-1f3b-4f6e-9d8e-2b6a1c9f7e10'],
];
$cookies = ['session' => 'abc123', 'theme' => 'dark'];

// The head as kest sends it for GET /?name=bench (internal/rrhttp).
$head = match ($codec) {
    'json' => json_encode([
        'remoteAddr' => '127.0.0.1',
        'protocol' => 'HTTP/1.1',
        'method' => 'GET',
        'uri' => 'http://127.0.0.1:18080/?name=bench',
        'headers' => $headers,
        'cookies' => $cookies,
        'rawQuery' => 'name=bench',
        'parsed' => false,
        'uploads' => null,
        'attributes' => new stdClass(),
    ], JSON_THROW_ON_ERROR),
    'proto' => (new RequestProto([
        'remote_addr' => '127.0.0.1',
        'protocol' => 'HTTP/1.1',
        'method' => 'GET',
        'uri' => 'http://127.0.0.1:18080/?name=bench',
        'header' => array_map(static fn(array $v) => new HeaderValue(['value' => $v]), $headers),
        'cookies' => array_map(static fn(string $v) => new HeaderValue(['value' => [$v]]), $cookies),
        'raw_query' => 'name=bench',
    ]))->serializeToString(),
    default => exit("usage: php codec.php json|proto [requests]\n"),
};

$relay = new class (new Payload('', $head)) implements WorkerInterface {
    public function __construct(private readonly Payload $payload) {}

    public function waitPayload(): ?Payload
    {
        return $this->payload;
    }

    public function respond(Payload $payload): void {}

    public function error(string $error): void {}

    public function stop(): void {}

    public function hasPayload(?string $class = null): bool
    {
        return false;
    }

    public function getPayload(?string $class = null): ?Payload
    {
        return null;
    }
};

$factory = new Psr17Factory();
$worker = new PSR7Worker($relay, $factory, $factory, $factory);

$serve = static function (int $requests) use ($worker): void {
    for ($i = 0; $i < $requests; $i++) {
        $name = $worker->waitRequest()->getQueryParams()['name'] ?? 'world';
        $worker->respond(new Response(200, ['Content-Type' => 'text/plain; charset=utf-8'], "Hello, $name!\n"));
    }
};

$serve(intdiv($requests, 10)); // warm-up
$start = hrtime(true);
$serve($requests);

printf("%s: %.1f µs per request (%d-byte head)\n", $codec, (hrtime(true) - $start) / 1e3 / $requests, strlen($head));
