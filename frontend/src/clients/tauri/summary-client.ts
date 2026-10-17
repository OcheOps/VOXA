import { invoke } from '@tauri-apps/api/core';
import type {
  SummaryCancellationResult, SummaryCapabilities, SummaryClient,
  SummaryGenerationResult, SummaryRequest, SummaryRuntimeInput,
  SummaryRuntimeReadiness, SummaryState, SummaryStatus, SummaryTemplate,
} from '../contracts';

interface NativeSummaryState {
  status: SummaryStatus;
  meetingName: string | null;
  meeting_id: string;
  start: string | null;
  end: string | null;
  data: unknown | null;
  error: string | null;
}

interface NativeGenerationResult { message: string; process_id: string; }
interface NativeCancellationResult { cancelled: boolean; message: string; meeting_id: string; }

export class TauriSummaryClient implements SummaryClient {
  readonly capabilities: SummaryCapabilities = {
    execution: 'local-and-remote', asynchronousJobs: true, polling: true,
    cancellation: true, templates: true, localRuntimePreflight: true,
    providers: [
      'builtin-ai', 'ollama', 'openai', 'claude', 'groq', 'openrouter', 'custom-openai',
    ],
  };

  async getState(meetingId: string): Promise<SummaryState> {
    const state = await invoke<NativeSummaryState>('api_get_summary', { meetingId });
    return {
      status: state.status, meetingName: state.meetingName, meetingId: state.meeting_id,
      processId: state.start, endedAt: state.end, data: state.data, error: state.error,
    };
  }

  async generate(request: SummaryRequest): Promise<SummaryGenerationResult> {
    const result = await invoke<NativeGenerationResult>('api_process_transcript', {
      text: request.text,
      model: request.provider,
      modelName: request.modelName,
      meetingId: request.meetingId,
      chunkSize: request.chunkSize,
      overlap: request.overlap,
      customPrompt: request.customPrompt,
      templateId: request.templateId,
      summaryLanguage: request.summaryLanguage,
    });
    return { message: result.message, processId: result.process_id };
  }

  async cancel(meetingId: string, processId: string): Promise<SummaryCancellationResult> {
    const result = await invoke<NativeCancellationResult>('api_cancel_summary', {
      meetingId, processId,
    });
    return { cancelled: result.cancelled, message: result.message, meetingId: result.meeting_id };
  }

  async save(meetingId: string, summary: unknown): Promise<void> {
    await invoke('api_save_meeting_summary', { meetingId, summary });
  }

  listTemplates(): Promise<SummaryTemplate[]> {
    return invoke('api_list_templates');
  }

  async checkRuntime(input: SummaryRuntimeInput): Promise<SummaryRuntimeReadiness> {
    if (input.provider === 'ollama') {
      const models = await invoke<unknown[]>('get_ollama_models', {
        endpoint: input.ollamaEndpoint,
      });
      return models.length > 0 ? { ready: true } : { ready: false, reason: 'no-models-available' };
    }
    if (input.provider === 'builtin-ai') {
      if (!input.modelName) return { ready: false, reason: 'no-model-selected' };
      const ready = await invoke<boolean>('builtin_ai_is_model_ready', {
        modelName: input.modelName,
        refresh: true,
      });
      return ready ? { ready: true } : { ready: false, reason: 'model-not-ready' };
    }
    return { ready: true };
  }
}
