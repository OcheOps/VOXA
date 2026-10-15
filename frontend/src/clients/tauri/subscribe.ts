import { listen, type UnlistenFn } from '@tauri-apps/api/event';

export function subscribeToTauriEvent<TNative, TFrontend = TNative>(
  eventName: string,
  callback: (payload: TFrontend) => void,
  map: (payload: TNative) => TFrontend = payload => payload as unknown as TFrontend,
  onRegistrationError?: (error: unknown) => void,
): () => void {
  let cancelled = false;
  let unlisten: UnlistenFn | undefined;

  void listen<TNative>(eventName, event => {
    if (!cancelled) callback(map(event.payload));
  }).then(registeredUnlisten => {
    if (cancelled) registeredUnlisten();
    else unlisten = registeredUnlisten;
  }, error => {
    if (!cancelled) {
      if (onRegistrationError) onRegistrationError(error);
      else console.error(`Failed to subscribe to ${eventName}:`, error);
    }
  });

  return () => {
    if (cancelled) return;
    cancelled = true;
    unlisten?.();
    unlisten = undefined;
  };
}

export function combineSubscriptions(...subscriptions: Array<() => void>): () => void {
  let cancelled = false;
  return () => {
    if (cancelled) return;
    cancelled = true;
    subscriptions.forEach(unsubscribe => unsubscribe());
  };
}
