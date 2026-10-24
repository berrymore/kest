# Symfony smoke test

A minimal Symfony 7.4 application (micro kernel, one controller) on the RoadRunner runtime
[`runtime/roadrunner-symfony-nyholm`](https://github.com/php-runtime/roadrunner-symfony-nyholm),
unchanged, served by kest. `make php-deps` installs it; `test/e2e/symfony_test.go` runs it.

`composer.json` pins the platform to PHP 8.3, so the lock file works on every PHP version kest is
tested with (8.3, 8.4, 8.5). The kernel writes its cache to `$APP_CACHE_DIR` (default: the system
temp directory).
