/** Bound the entire request, including body parsing, even if the transport ignores abort. */
export async function withRequestTimeout<T>(
  signal: AbortSignal | undefined,
  timeoutMs: number,
  cancellationError: (timedOut: boolean) => Error,
  request: (signal: AbortSignal) => Promise<T>,
): Promise<T> {
  if (signal?.aborted) throw cancellationError(false);

  const controller = new AbortController();
  let timedOut = false;
  const aborted = new Promise<never>((_, reject) => {
    controller.signal.addEventListener('abort', () => reject(cancellationError(timedOut)), { once: true });
  });
  const cancel = () => controller.abort();
  signal?.addEventListener('abort', cancel, { once: true });
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs);

  try {
    return await Promise.race([aborted, request(controller.signal)]);
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener('abort', cancel);
  }
}
