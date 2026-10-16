import { useState, useEffect, useRef } from 'react';
import { type MediaSource, useVoxaClient } from '@/clients';

export const useAudioPlayer = (mediaSource: MediaSource | null) => {
  const { media: mediaClient } = useVoxaClient();
  const [isPlaying, setIsPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const audioRef = useRef<AudioContext | null>(null);
  const sourceRef = useRef<AudioBufferSourceNode | null>(null);
  const startTimeRef = useRef<number>(0);
  const audioBufferRef = useRef<AudioBuffer | null>(null);
  const rafRef = useRef<number>();
  const seekTimeRef = useRef<number>(0);
  const loadRequestRef = useRef(0);

  const initAudioContext = async () => {
    try {
      if (!audioRef.current) {
        console.log('Creating new AudioContext');
        const AudioContextClass = window.AudioContext || (window as any).webkitAudioContext;
        audioRef.current = new AudioContextClass();
        console.log('AudioContext created:', {
          state: audioRef.current.state,
          sampleRate: audioRef.current.sampleRate,
        });
      }

      if (audioRef.current.state === 'suspended') {
        console.log('Resuming suspended AudioContext');
        await audioRef.current.resume();
        console.log('AudioContext resumed:', audioRef.current.state);
      }
      
      setError(null);
      return true;
    } catch (error) {
      console.error('Error initializing AudioContext:', error);
      setError('Failed to initialize audio');
      return false;
    }
  };

  // Cleanup function
  useEffect(() => {
    return () => {
      console.log('Cleaning up audio resources');
      if (rafRef.current) {
        cancelAnimationFrame(rafRef.current);
      }
      if (sourceRef.current) {
        sourceRef.current.stop();
      }
      if (audioRef.current) {
        audioRef.current.close();
      }
    };
  }, []);

  const loadAudio = async (
    source: MediaSource,
    requestId: number,
    signal: AbortSignal,
  ) => {
    try {
      // Initialize context first
      const initialized = await initAudioContext();
      if (!initialized || !audioRef.current) {
        console.error('Failed to initialize audio context');
        return;
      }

      const playbackSource = await mediaClient.getPlaybackSource(source);
      if (requestId !== loadRequestRef.current) return;
      const audioData = playbackSource.kind === 'bytes'
        ? new Uint8Array(playbackSource.bytes).buffer
        : await fetch(playbackSource.url, { signal }).then(response => {
            if (!response.ok) throw new Error(`Media request failed: ${response.status}`);
            return response.arrayBuffer();
          });

      if (requestId !== loadRequestRef.current) return;

      if (audioData.byteLength === 0) {
        throw new Error('Empty audio data received');
      }
      
      console.log('Created audio buffer, size:', audioData.byteLength, 'bytes');
      
      // Decode the audio data
      const audioBuffer = await new Promise<AudioBuffer>((resolve, reject) => {
        audioRef.current!.decodeAudioData(
          audioData,
          buffer => {
            console.log('Audio decoded successfully:', {
              duration: buffer.duration,
              sampleRate: buffer.sampleRate,
              numberOfChannels: buffer.numberOfChannels,
              length: buffer.length
            });
            resolve(buffer);
          },
          error => {
            console.error('Audio decoding failed:', error);
            reject(new Error('Failed to decode audio data: ' + error));
          }
        );
      });
      
      if (requestId !== loadRequestRef.current) return;
      audioBufferRef.current = audioBuffer;
      setDuration(audioBuffer.duration);
      setCurrentTime(0);
      setError(null);
      console.log('Audio loaded and ready to play');
    } catch (error) {
      if (signal.aborted || requestId !== loadRequestRef.current) return;
      console.error('Error loading audio:', error);
      if (error instanceof Error) {
        console.error('Error details:', {
          message: error.message,
          name: error.name,
          stack: error.stack,
        });
      }
      setError('Failed to load audio file');
    }
  };

  // Load audio when the opaque media source changes
  useEffect(() => {
    const requestId = ++loadRequestRef.current;
    const controller = new AbortController();

    if (rafRef.current) {
      cancelAnimationFrame(rafRef.current);
      rafRef.current = undefined;
    }
    if (sourceRef.current) {
      try {
        sourceRef.current.stop();
        sourceRef.current.disconnect();
      } catch {
        // The source may already have stopped naturally.
      }
      sourceRef.current = null;
    }
    audioBufferRef.current = null;
    seekTimeRef.current = 0;
    setIsPlaying(false);
    setDuration(0);
    setCurrentTime(0);

    if (mediaSource) {
      void loadAudio(mediaSource, requestId, controller.signal);
    }

    return () => {
      controller.abort();
      if (loadRequestRef.current === requestId) loadRequestRef.current += 1;
    };
  }, [mediaClient, mediaSource]);

  const stopPlayback = () => {
    console.log('Stopping playback');
    if (rafRef.current) {
      cancelAnimationFrame(rafRef.current);
      rafRef.current = undefined;
    }
    if (sourceRef.current) {
      try {
        sourceRef.current.stop();
        sourceRef.current.disconnect();
      } catch (e) {
        console.log('Error stopping source:', e);
      }
      sourceRef.current = null;
    }
    setIsPlaying(false);
  };

  const play = async () => {
    console.log('Play requested');
    
    try {
      // Initialize context if needed
      const initialized = await initAudioContext();
      if (!initialized) {
        throw new Error('Audio context initialization failed');
      }
      if (!audioRef.current) {
        throw new Error('Audio context is null after initialization');
      }
      if (!audioBufferRef.current) {
        throw new Error('No audio buffer loaded - try loading the audio file first');
      }
      if (audioRef.current.state !== 'running') {
        throw new Error(`Audio context is in invalid state: ${audioRef.current.state}`);
      }

      // Stop any existing playback
      stopPlayback();

      // Create and setup new source
      console.log('Creating new audio source');
      sourceRef.current = audioRef.current.createBufferSource();
      sourceRef.current.buffer = audioBufferRef.current;
      
      console.log('Audio buffer details:', {
        duration: audioBufferRef.current.duration,
        sampleRate: audioBufferRef.current.sampleRate,
        numberOfChannels: audioBufferRef.current.numberOfChannels,
        length: audioBufferRef.current.length
      });
      
      sourceRef.current.connect(audioRef.current.destination);
      
      // Setup ended callback
      sourceRef.current.onended = () => {
        console.log('Playback ended naturally');
        stopPlayback();
        setCurrentTime(0);
      };
      
      // Start playback from the seek time
      const startTime = seekTimeRef.current;
      startTimeRef.current = audioRef.current.currentTime - startTime;
      console.log('Starting playback', {
        startTime,
        contextTime: audioRef.current.currentTime,
        seekTime: seekTimeRef.current
      });
      
      sourceRef.current.start(0, startTime);
      setIsPlaying(true);
      setError(null);

      // Setup time update
      const updateTime = () => {
        if (!audioRef.current || !sourceRef.current) {
          console.log('Update cancelled - context or source is null');
          return;
        }
        
        const newTime = audioRef.current.currentTime - startTimeRef.current;
        
        if (newTime >= duration) {
          console.log('Playback finished');
          stopPlayback();
          setCurrentTime(0);
          seekTimeRef.current = 0;
        } else {
          setCurrentTime(newTime);
          seekTimeRef.current = newTime;
          rafRef.current = requestAnimationFrame(updateTime);
        }
      };
      
      rafRef.current = requestAnimationFrame(updateTime);
    } catch (error) {
      console.error('Error during playback:', error);
      setError('Failed to play audio');
      stopPlayback();
    }
  };

  const seek = async (time: number) => {
    console.log('Seek requested:', time);
    if (time < 0) time = 0;
    if (time > duration) time = duration;
    
    const wasPlaying = isPlaying;
    
    // Stop current playback
    stopPlayback();
    
    // Update both current time and seek time reference
    seekTimeRef.current = time;
    setCurrentTime(time);
    
    // If it was playing before, restart playback at new position
    if (wasPlaying) {
      console.log('Restarting playback at:', time);
      await play();
    }
  };

  const pause = () => {
    console.log('Pause requested');
    stopPlayback();
  };

  return {
    isPlaying,
    currentTime,
    duration,
    error,
    play,
    pause,
    seek
  };
};
