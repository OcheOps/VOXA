import { useState, useEffect, useCallback } from 'react';
import { toast } from 'sonner';
import Analytics from '@/lib/analytics';
import { type SummaryTemplate, useVoxaClient } from '@/clients';

export function useTemplates() {
  const { summaries: summaryClient } = useVoxaClient();
  const [availableTemplates, setAvailableTemplates] = useState<SummaryTemplate[]>([]);
  const [selectedTemplate, setSelectedTemplate] = useState<string>('standard_meeting');

  // Fetch available templates on mount
  useEffect(() => {
    const fetchTemplates = async () => {
      try {
        const templates = await summaryClient.listTemplates();
        console.log('Available templates:', templates);
        setAvailableTemplates(templates);
      } catch (error) {
        console.error('Failed to fetch templates:', error);
      }
    };
    fetchTemplates();
  }, [summaryClient]);

  // Handle template selection
  const handleTemplateSelection = useCallback((templateId: string, templateName: string) => {
    setSelectedTemplate(templateId);
    toast.success('Template selected', {
      description: `Using "${templateName}" template for summary generation`,
    });
    Analytics.trackFeatureUsed('template_selected');
  }, []);

  return {
    availableTemplates,
    selectedTemplate,
    handleTemplateSelection,
  };
}
