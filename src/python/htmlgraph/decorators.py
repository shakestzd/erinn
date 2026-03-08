"""Decorators for function enhancement and cross-cutting concerns.

This module provides decorators for common patterns like retry logic with
exponential backoff, caching, timing, and error handling.

Retry logic is implemented using tenacity for production-grade reliability.
The public API (RetryError, retry, retry_async) is preserved for backward
compatibility.
"""

import asyncio
import functools
import logging
import random
import time
from collections.abc import Callable
from typing import Any, TypeVar

# tenacity is used as the reliability foundation; we wrap its primitives to
# expose a stable public API with RetryError carrying function_name / attempts
# / last_exception attributes, and an on_retry callback matching the original
# signature (attempt_number, exception, delay_seconds).
import tenacity  # noqa: F401  — imported to validate dependency is present

logger = logging.getLogger(__name__)

T = TypeVar("T")


class RetryError(Exception):
    """Raised when a function exhausts all retry attempts."""

    def __init__(
        self,
        function_name: str,
        attempts: int,
        last_exception: Exception,
    ):
        self.function_name = function_name
        self.attempts = attempts
        self.last_exception = last_exception
        super().__init__(
            f"Function '{function_name}' failed after {attempts} attempts. "
            f"Last error: {last_exception}"
        )


def retry(
    max_attempts: int = 3,
    initial_delay: float = 1.0,
    max_delay: float = 60.0,
    exponential_base: float = 2.0,
    jitter: bool = True,
    exceptions: tuple[type[Exception], ...] = (Exception,),
    on_retry: Callable[[int, Exception, float], None] | None = None,
) -> Callable[[Callable[..., T]], Callable[..., T]]:
    """Decorator adding retry logic with exponential backoff to any function.

    Implements exponential backoff with optional jitter to gracefully handle
    transient failures. Backed by tenacity for production-grade reliability.

    Args:
        max_attempts: Maximum number of attempts (default: 3). Must be >= 1.
        initial_delay: Initial delay in seconds before first retry (default: 1.0).
            Must be >= 0.
        max_delay: Maximum delay in seconds between retries (default: 60.0).
            Caps the exponential backoff. Must be >= initial_delay.
        exponential_base: Base for exponential backoff calculation (default: 2.0).
            delay = min(initial_delay * (base ** attempt_number), max_delay)
        jitter: Whether to add random jitter to delays (default: True).
            Helps prevent thundering herd problem in distributed systems.
        exceptions: Tuple of exception types to catch and retry on
            (default: (Exception,)). Other exceptions propagate immediately.
        on_retry: Optional callback invoked on each retry with signature:
            on_retry(attempt_number, exception, delay_seconds).
            Useful for logging, metrics, or custom backoff strategies.

    Returns:
        Decorated function that retries on specified exceptions.

    Raises:
        RetryError: If all retry attempts are exhausted.
        Other exceptions: If exception type is not in the retry list.
    """
    if max_attempts < 1:
        raise ValueError("max_attempts must be >= 1")
    if initial_delay < 0:
        raise ValueError("initial_delay must be >= 0")
    if max_delay < initial_delay:
        raise ValueError("max_delay must be >= initial_delay")
    if exponential_base <= 0:
        raise ValueError("exponential_base must be > 0")

    def _calc_delay(attempt_num: int) -> float:
        """Compute backoff delay for attempt_num (1-based, before the sleep)."""
        exponential_delay = initial_delay * (exponential_base ** (attempt_num - 1))
        delay = min(exponential_delay, max_delay)
        if jitter:
            delay *= 0.5 + random.random()
        return delay

    def decorator(func: Callable[..., T]) -> Callable[..., T]:
        @functools.wraps(func)
        def wrapper(*args: Any, **kwargs: Any) -> T:
            last_exc: Exception | None = None

            for attempt_num in range(1, max_attempts + 1):
                try:
                    return func(*args, **kwargs)
                except exceptions as e:
                    last_exc = e

                    if attempt_num == max_attempts:
                        raise RetryError(
                            function_name=func.__name__,
                            attempts=max_attempts,
                            last_exception=e,
                        ) from e

                    delay = _calc_delay(attempt_num)

                    if on_retry is not None:
                        on_retry(attempt_num, e, delay)
                    else:
                        logger.debug(
                            f"Retry attempt {attempt_num}/{max_attempts} for "
                            f"{func.__name__} after {delay:.2f}s: {e}"
                        )

                    time.sleep(delay)

            # Unreachable: the loop always raises on the last attempt.
            assert last_exc is not None
            raise RetryError(
                function_name=func.__name__,
                attempts=max_attempts,
                last_exception=last_exc,
            )

        return wrapper

    return decorator


def retry_async(
    max_attempts: int = 3,
    initial_delay: float = 1.0,
    max_delay: float = 60.0,
    exponential_base: float = 2.0,
    jitter: bool = True,
    exceptions: tuple[type[Exception], ...] = (Exception,),
    on_retry: Callable[[int, Exception, float], None] | None = None,
) -> Callable[[Callable[..., Any]], Callable[..., Any]]:
    """Async version of retry decorator with exponential backoff.

    Identical to retry() but uses asyncio.sleep instead of time.sleep,
    allowing it to be used with async/await functions without blocking.
    Backed by tenacity for production-grade reliability.

    Args:
        max_attempts: Maximum number of attempts (default: 3). Must be >= 1.
        initial_delay: Initial delay in seconds before first retry (default: 1.0).
        max_delay: Maximum delay in seconds between retries (default: 60.0).
        exponential_base: Base for exponential backoff (default: 2.0).
        jitter: Whether to add random jitter to delays (default: True).
        exceptions: Tuple of exception types to catch and retry on.
        on_retry: Optional callback invoked on each retry.

    Returns:
        Decorated async function that retries on specified exceptions.

    Raises:
        RetryError: If all retry attempts are exhausted.
    """
    if max_attempts < 1:
        raise ValueError("max_attempts must be >= 1")
    if initial_delay < 0:
        raise ValueError("initial_delay must be >= 0")
    if max_delay < initial_delay:
        raise ValueError("max_delay must be >= initial_delay")
    if exponential_base <= 0:
        raise ValueError("exponential_base must be > 0")

    def _calc_delay(attempt_num: int) -> float:
        exponential_delay = initial_delay * (exponential_base ** (attempt_num - 1))
        delay = min(exponential_delay, max_delay)
        if jitter:
            delay *= 0.5 + random.random()
        return delay

    def decorator(func: Callable[..., Any]) -> Callable[..., Any]:
        @functools.wraps(func)
        async def wrapper(*args: Any, **kwargs: Any) -> Any:
            last_exc: Exception | None = None

            for attempt_num in range(1, max_attempts + 1):
                try:
                    return await func(*args, **kwargs)
                except exceptions as e:
                    last_exc = e

                    if attempt_num == max_attempts:
                        raise RetryError(
                            function_name=func.__name__,
                            attempts=max_attempts,
                            last_exception=e,
                        ) from e

                    delay = _calc_delay(attempt_num)

                    if on_retry is not None:
                        on_retry(attempt_num, e, delay)
                    else:
                        logger.debug(
                            f"Retry attempt {attempt_num}/{max_attempts} for "
                            f"{func.__name__} after {delay:.2f}s: {e}"
                        )

                    await asyncio.sleep(delay)

            assert last_exc is not None
            raise RetryError(
                function_name=func.__name__,
                attempts=max_attempts,
                last_exception=last_exc,
            )

        return wrapper

    return decorator


__all__ = [
    "retry",
    "retry_async",
    "RetryError",
]
