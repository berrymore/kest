<?php

declare(strict_types=1);

namespace App;

use Symfony\Bundle\FrameworkBundle\FrameworkBundle;
use Symfony\Bundle\FrameworkBundle\Kernel\MicroKernelTrait;
use Symfony\Component\DependencyInjection\Loader\Configurator\ContainerConfigurator;
use Symfony\Component\HttpKernel\Kernel as BaseKernel;
use Symfony\Component\Routing\Loader\Configurator\RoutingConfigurator;

final class Kernel extends BaseKernel
{
    use MicroKernelTrait;

    public function registerBundles(): iterable
    {
        yield new FrameworkBundle();
    }

    public function getCacheDir(): string
    {
        return ($_SERVER['APP_CACHE_DIR'] ?? sys_get_temp_dir() . '/kest-symfony') . '/' . $this->environment;
    }

    public function getLogDir(): string
    {
        return $this->getCacheDir() . '/log';
    }

    protected function configureContainer(ContainerConfigurator $container): void
    {
        $container->extension('framework', [
            'secret' => 'kest-smoke-test',
            'http_method_override' => false,
            'handle_all_throwables' => true,
            'php_errors' => ['log' => true],
            'router' => ['utf8' => true],
        ]);
        $container->services()
            ->load('App\\Controller\\', __DIR__ . '/Controller/')
            ->autowire()
            ->autoconfigure();
    }

    protected function configureRoutes(RoutingConfigurator $routes): void
    {
        $routes->import(__DIR__ . '/Controller/', 'attribute');
    }
}
