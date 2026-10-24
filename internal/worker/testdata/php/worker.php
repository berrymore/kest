<?php

// An unmodified roadrunner/worker loop for kest's integration tests
// (internal/worker/php_integration_test.go). The request body is a command:
//
//   pid         respond with the worker's pid
//   error       report a soft error ($worker->error)
//   stop        ask to be replaced ($worker->stop)
//   grow <MB>   keep <MB> MiB allocated, to test max_worker_memory
//   sleep <ms>  sleep, then echo
//   anything    echo: the context as header, the body as body

declare(strict_types=1);

require __DIR__ . '/vendor/autoload.php';

use Spiral\RoadRunner\Payload;
use Spiral\RoadRunner\Worker;

$worker = Worker::create();
fwrite(STDERR, "php worker booted\n");

$ballast = [];
while ($request = $worker->waitPayload()) {
    [$command, $argument] = explode(' ', $request->body, 2) + [1 => ''];
    switch ($command) {
        case 'pid':
            $worker->respond(new Payload((string) getmypid(), $request->header));
            break;
        case 'error':
            $worker->error('php error');
            break;
        case 'stop':
            $worker->stop();
            break;
        case 'grow':
            $ballast[] = str_repeat('x', (int) $argument << 20);
            $worker->respond(new Payload($request->body, $request->header));
            break;
        case 'sleep':
            usleep((int) $argument * 1000);
            $worker->respond(new Payload($request->body, $request->header));
            break;
        default:
            $worker->respond(new Payload($request->body, $request->header));
    }
}
