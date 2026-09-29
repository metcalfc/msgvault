/**
 * The open reading pane's thread registers how to step to the previous or
 * next message, so the app-wide h/l and ←/→ shortcuts can move within the
 * thread without the shell knowing the thread's messages. Only the most
 * recently mounted thread steps.
 */
type Stepper = (delta: number) => boolean;

let active: Stepper | undefined;

export function registerThreadStepper(stepper: Stepper): () => void {
  active = stepper;
  return () => {
    if (active === stepper) active = undefined;
  };
}

/** Steps the open thread; false when no thread is open or it cannot move. */
export function stepThread(delta: number): boolean {
  return active?.(delta) ?? false;
}
