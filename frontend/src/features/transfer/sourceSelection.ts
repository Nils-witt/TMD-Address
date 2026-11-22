import type { SourceFilter } from '../../api/types';

/** What the fields edit: every row, or the chosen sources. */
export interface SourceSelection {
  scope: 'all' | 'selected';
  manual: boolean;
  collectors: string[];
}

export const ALL_SOURCES: SourceSelection = { scope: 'all', manual: false, collectors: [] };

/** The filter the API takes for `selection`. */
export function toSourceFilter(selection: SourceSelection): SourceFilter {
  return selection.scope === 'all'
    ? { manual: false, collectors: [] }
    : { manual: selection.manual, collectors: selection.collectors };
}

/** Whether `selection` names something to cover: "selected" with nothing
 * chosen would cover every row, which "all" is for. */
export function isSourceSelectionValid(selection: SourceSelection): boolean {
  return selection.scope === 'all' || selection.manual || selection.collectors.length > 0;
}
