<?php

declare(strict_types=1);

namespace App\Controller;

use Symfony\Component\HttpFoundation\JsonResponse;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\HttpFoundation\Response;
use Symfony\Component\Routing\Attribute\Route;

final class SmokeController
{
    private int $served = 0;

    #[Route('/hello/{name}', methods: ['GET'])]
    public function hello(string $name, Request $request): JsonResponse
    {
        $this->served++;

        return new JsonResponse([
            'hello' => $name,
            'query' => $request->query->all(),
            'host' => $request->getHost(),
            'scheme' => $request->getScheme(),
            'client_ip' => $request->getClientIp(),
            'cookies' => $request->cookies->all(),
            'served_by_this_worker' => $this->served,
            'pid' => getmypid(),
        ]);
    }

    #[Route('/form', methods: ['POST'])]
    public function form(Request $request): JsonResponse
    {
        $files = [];
        foreach ($request->files->all() as $name => $file) {
            $files[$name] = ['name' => $file->getClientOriginalName(), 'content' => $file->getContent()];
        }

        return new JsonResponse(['fields' => $request->request->all(), 'files' => $files]);
    }

    #[Route('/redirect', methods: ['GET'])]
    public function redirect(): Response
    {
        $response = new Response('', Response::HTTP_FOUND, ['Location' => '/hello/redirected']);
        $response->headers->setCookie(new \Symfony\Component\HttpFoundation\Cookie('visited', 'yes'));

        return $response;
    }

    #[Route('/boom', methods: ['GET'])]
    public function boom(): Response
    {
        throw new \RuntimeException('boom');
    }
}
