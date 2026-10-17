import { beforeEach, describe, expect, mock, test } from 'bun:test';

type InvokeCall = { command: string; args?: Record<string, unknown> };
const calls: InvokeCall[] = [];

const invoke = mock(async (command: string, args?: Record<string, unknown>): Promise<unknown> => {
  calls.push({ command, args });
  if (command === 'api_get_summary') return {
    status: 'processing', meetingName: 'Planning', meeting_id: 'meeting-1',
    start: 'job-1', end: null, data: null, error: null,
  };
  if (command === 'api_process_transcript') return { message: 'started', process_id: 'job-1' };
  if (command === 'api_cancel_summary') {
    return { cancelled: true, message: 'cancelled', meeting_id: 'meeting-1' };
  }
  if (command === 'api_list_templates') {
    return [{ id: 'standard_meeting', name: 'Standard', description: 'General meeting' }];
  }
  if (command === 'get_ollama_models') return [{ name: 'gemma3:1b' }];
  if (command === 'builtin_ai_is_model_ready') return true;
  return undefined;
});

mock.module('@tauri-apps/api/core', () => ({ invoke }));
const { TauriSummaryClient } = await import('../../src/clients/tauri/summary-client');

beforeEach(() => {
  calls.length = 0;
  invoke.mockClear();
});

describe('TauriSummaryClient', () => {
  test('reports factual desktop summary capabilities and providers', () => {
    expect(new TauriSummaryClient().capabilities).toEqual({
      execution: 'local-and-remote', asynchronousJobs: true, polling: true,
      cancellation: true, templates: true, localRuntimePreflight: true,
      providers: [
        'builtin-ai', 'ollama', 'openai', 'claude', 'groq', 'openrouter', 'custom-openai',
      ],
    });
  });

  test('normalizes summary job state and preserves its native query', async () => {
    const client = new TauriSummaryClient();
    expect(await client.getState('meeting-1')).toEqual({
      status: 'processing', meetingName: 'Planning', meetingId: 'meeting-1',
      processId: 'job-1', endedAt: null, data: null, error: null,
    });
    expect(calls).toEqual([{
      command: 'api_get_summary', args: { meetingId: 'meeting-1' },
    }]);
  });

  test('preserves generation payload semantics and normalizes the process id', async () => {
    const client = new TauriSummaryClient();
    const request = {
      text: '[00:00] Hello', provider: 'openai' as const, modelName: 'gpt-4o-mini',
      meetingId: 'meeting-1', chunkSize: 40000, overlap: 1000,
      customPrompt: 'Focus on decisions', templateId: 'standard_meeting',
      summaryLanguage: 'en',
    };
    expect(await client.generate(request)).toEqual({ message: 'started', processId: 'job-1' });
    expect(calls).toEqual([{ command: 'api_process_transcript', args: {
      text: request.text, model: 'openai', modelName: request.modelName,
      meetingId: request.meetingId, chunkSize: 40000, overlap: 1000,
      customPrompt: request.customPrompt, templateId: request.templateId,
      summaryLanguage: request.summaryLanguage,
    } }]);
  });

  test('normalizes cancellation and preserves save payloads', async () => {
    const client = new TauriSummaryClient();
    expect(await client.cancel('meeting-1', 'job-1')).toEqual({
      cancelled: true, message: 'cancelled', meetingId: 'meeting-1',
    });
    await client.save('meeting-1', { markdown: '# Notes' });
    expect(calls).toEqual([
      { command: 'api_cancel_summary', args: { meetingId: 'meeting-1', processId: 'job-1' } },
      { command: 'api_save_meeting_summary', args: {
        meetingId: 'meeting-1', summary: { markdown: '# Notes' },
      } },
    ]);
  });

  test('normalizes runtime templates', async () => {
    expect(await new TauriSummaryClient().listTemplates()).toEqual([
      { id: 'standard_meeting', name: 'Standard', description: 'General meeting' },
    ]);
    expect(calls).toEqual([{ command: 'api_list_templates', args: undefined }]);
  });

  test('checks only provider readiness required by active execution', async () => {
    const client = new TauriSummaryClient();
    expect(await client.checkRuntime({
      provider: 'ollama', modelName: 'gemma3:1b', ollamaEndpoint: 'http://localhost:11434',
    })).toEqual({ ready: true });
    expect(await client.checkRuntime({
      provider: 'builtin-ai', modelName: 'qwen.gguf', ollamaEndpoint: null,
    })).toEqual({ ready: true });
    expect(await client.checkRuntime({
      provider: 'claude', modelName: 'claude-sonnet', ollamaEndpoint: null,
    })).toEqual({ ready: true });
    expect(calls).toEqual([
      { command: 'get_ollama_models', args: { endpoint: 'http://localhost:11434' } },
      { command: 'builtin_ai_is_model_ready', args: { modelName: 'qwen.gguf', refresh: true } },
    ]);
  });

  test('reports existing provider preflight failures without changing native behavior', async () => {
    invoke.mockImplementationOnce(async () => []);
    const client = new TauriSummaryClient();
    expect(await client.checkRuntime({
      provider: 'ollama', modelName: 'gemma3:1b', ollamaEndpoint: null,
    })).toEqual({ ready: false, reason: 'no-models-available' });
    expect(await client.checkRuntime({
      provider: 'builtin-ai', modelName: '', ollamaEndpoint: null,
    })).toEqual({ ready: false, reason: 'no-model-selected' });
  });

  test('propagates native summary errors', async () => {
    invoke.mockImplementationOnce(async () => { throw new Error('processor unavailable'); });
    await expect(new TauriSummaryClient().getState('meeting-1'))
      .rejects.toThrow('processor unavailable');
  });
});
