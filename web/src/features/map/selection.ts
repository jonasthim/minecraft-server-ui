import type { components } from '../../api/schema';

type Summary = components['schemas']['MapSummary'];
type View = components['schemas']['MapView'];
type Selection = { summary: Summary | null; dimension: string; view: View | null };
type Action = { type: 'summary'; value: Summary } | { type: 'dimension'; value: string } | { type: 'view'; dimension: string; value: View } | { type: 'clear_view' };
export const initialSelection: Selection = { summary: null, dimension: '', view: null };
export function reduceSelection(state: Selection, action: Action): Selection {
  switch (action.type) {
    case 'summary': {
      const dimension = action.value.discovery.dimensions.some(d => d.id === state.dimension) ? state.dimension : action.value.discovery.dimensions[0]?.id || '';
      return { ...state, summary: action.value, dimension, view: dimension === state.dimension ? state.view : null };
    }
    case 'dimension': return { ...state, dimension: action.value, view: null };
    case 'view': return action.dimension === state.dimension ? { ...state, view: action.value } : state;
    case 'clear_view': return { ...state, view: null };
  }
}
