// The current API collapses legacy DOM `submitting` rows into `pending` and
// exposes their unknown reward as numeric zero. Keep this compatibility rule
// narrowly scoped so a real zero-reward block on another coin is unchanged.
export function isUnfinishedDOMCandidate(block, coin) {
  return String(coin?.id ?? '').toLowerCase() === 'dom'
    && String(block?.status ?? '').toLowerCase() === 'pending'
    && typeof block?.reward === 'number'
    && Number.isFinite(block.reward)
    && block.reward === 0;
}
