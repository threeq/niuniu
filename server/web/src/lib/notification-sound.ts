/**
 * Notification sound player using Web Audio API.
 *
 * HTMLAudioElement.play() is blocked by browser autoplay policy when the tab
 * is in the background. AudioContext, once resumed during a user gesture,
 * keeps working even when the tab loses focus.
 */

const SOUNDS = {
  cowMooing: '/assets/sounds/cow-mooing.wav',
  ding: '/assets/sounds/ding.wav',
  chime: '/assets/sounds/chime.wav',
  bell: '/assets/sounds/bell.wav',
  horse: '/assets/sounds/horse.wav',
  cat: '/assets/sounds/cat.wav',
  chicken: '/assets/sounds/chicken.wav',
  frog: '/assets/sounds/frog.wav',
  cricket: '/assets/sounds/cricket.wav',
  nightingale: '/assets/sounds/nightingale.wav',
  cuckoo: '/assets/sounds/cuckoo.wav',
  robin: '/assets/sounds/robin.wav',
  blackbird: '/assets/sounds/blackbird.wav',
  owl: '/assets/sounds/owl.wav',
} as const;

type SoundName = keyof typeof SOUNDS;

let ctx: AudioContext | null = null;
const bufferCache = new Map<string, AudioBuffer>();

function getContext(): AudioContext {
  if (!ctx) {
    ctx = new AudioContext();
  }
  return ctx;
}

// Resume AudioContext on first user interaction so it stays unlocked
// even when the tab goes to background later.
function ensureUnlocked() {
  const ac = getContext();
  if (ac.state === 'suspended') {
    ac.resume();
  }
}

const UNLOCK_EVENTS = ['click', 'keydown', 'touchstart', 'pointerdown'] as const;
let listenersAttached = false;

function attachUnlockListeners() {
  if (listenersAttached) return;
  listenersAttached = true;
  for (const event of UNLOCK_EVENTS) {
    document.addEventListener(event, ensureUnlocked, { once: false, capture: true });
  }
}

async function loadBuffer(url: string): Promise<AudioBuffer> {
  const cached = bufferCache.get(url);
  if (cached) return cached;

  const response = await fetch(url);
  const arrayBuffer = await response.arrayBuffer();
  const audioBuffer = await getContext().decodeAudioData(arrayBuffer);
  bufferCache.set(url, audioBuffer);
  return audioBuffer;
}

/**
 * Play a notification sound. Works even when the browser tab is in the background,
 * as long as the user has interacted with the page at least once.
 *
 * @param name  - which sound to play
 * @param volume - 0-100 (default 80)
 */
export async function playNotificationSound(
  name: SoundName = 'cowMooing',
  volume: number = 80,
): Promise<void> {
  const ac = getContext();

  // Try to resume if still suspended (shouldn't happen after user interaction)
  if (ac.state === 'suspended') {
    await ac.resume();
  }

  const buffer = await loadBuffer(SOUNDS[name]);
  const source = ac.createBufferSource();
  source.buffer = buffer;

  const gainNode = ac.createGain();
  gainNode.gain.value = Math.max(0, Math.min(1, volume / 100));
  source.connect(gainNode);
  gainNode.connect(ac.destination);

  source.start(0);
}

/**
 * Pre-load sound files so playback is instant when needed.
 */
export function preloadSounds(): void {
  for (const url of Object.values(SOUNDS)) {
    loadBuffer(url).catch(() => {});
  }
}

// Attach unlock listeners immediately on module load
attachUnlockListeners();

// Pre-load sounds so first playback is instant
preloadSounds();
