import { useCallback } from 'react';
import type { ImportOptions, ImportResult, SourceFilter } from '../api/types';
import { useApi } from './useApi';
import { useInvalidate } from './useInvalidate';

/** Saves `blob` as a file named `filename` through a temporary link. */
function saveFile(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  document.body.append(link);
  link.click();
  link.remove();
  // Revoked on the next tick: revoking right away can cancel the download.
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

/** Downloads exports and imports files, refreshing the cached lists after an
 * import that kept its changes. */
export function useTransfer() {
  const api = useApi();
  const invalidate = useInvalidate();

  const downloadExport = useCallback(
    async (filter: SourceFilter) => {
      const { blob, filename } = await api.exportData(filter);
      saveFile(blob, filename);
    },
    [api],
  );

  const importFile = useCallback(
    async (file: Blob, options: ImportOptions): Promise<ImportResult> => {
      const result = await api.importData(file, options);
      if (!result.dryRun) await invalidate('imported');
      return result;
    },
    [api, invalidate],
  );

  return { downloadExport, importFile };
}
