const NS = 'http://www.w3.org/2000/svg';

function svgElement(name, attributes = {}, text = '') {
  const element = document.createElementNS(NS, name);
  for (const [key, value] of Object.entries(attributes)) element.setAttribute(key, String(value));
  if (text) element.textContent = text;
  return element;
}

function validNumber(value) {
  return typeof value === 'number' && Number.isFinite(value);
}

export function nearestPointIndexByX(xValues, targetX) {
  if (!Array.isArray(xValues) || !xValues.length || !validNumber(targetX)) return -1;
  if (targetX <= xValues[0]) return 0;
  const lastIndex = xValues.length - 1;
  if (targetX >= xValues[lastIndex]) return lastIndex;
  let low = 0;
  let high = lastIndex;
  while (low <= high) {
    const middle = (low + high) >> 1;
    const value = xValues[middle];
    if (value === targetX) return middle;
    if (value < targetX) low = middle + 1;
    else high = middle - 1;
  }
  return targetX - xValues[high] <= xValues[low] - targetX ? high : low;
}

function clamp(value, minimum, maximum) {
  return Math.min(maximum, Math.max(minimum, value));
}

function anchoredTooltipPosition(anchorX, anchorY, boxWidth, boxHeight, viewportWidth, viewportHeight) {
  const padding = 8;
  const offset = 12;
  let x = anchorX + offset;
  if (x + boxWidth > viewportWidth - padding) x = anchorX - boxWidth - offset;
  let y = anchorY - boxHeight - offset;
  if (y < padding) y = anchorY + offset;
  return {
    x: clamp(x, padding, viewportWidth - boxWidth - padding),
    y: clamp(y, padding, viewportHeight - boxHeight - padding),
  };
}

function bindPlotPointerTracking(svg, xValues, bounds, showIndex, leavePlot) {
  let capturedPointerId = null;
  const pointFromEvent = (event, allowOutside = false) => {
    const rect = svg.getBoundingClientRect();
    if (!rect.width || !rect.height) return null;
    const viewX = ((event.clientX - rect.left) / rect.width) * bounds.viewWidth;
    const viewY = ((event.clientY - rect.top) / rect.height) * bounds.viewHeight;
    if (!allowOutside && (viewX < bounds.left || viewX > bounds.right || viewY < bounds.top || viewY > bounds.bottom)) return null;
    return clamp(viewX, bounds.left, bounds.right);
  };
  const update = (event, allowOutside = false) => {
    const pointerX = pointFromEvent(event, allowOutside);
    if (pointerX === null) { leavePlot(); return false; }
    const index = nearestPointIndexByX(xValues, pointerX);
    if (index >= 0) showIndex(index);
    return index >= 0;
  };
  svg.addEventListener('pointermove', (event) => update(event, capturedPointerId === event.pointerId));
  svg.addEventListener('pointerleave', () => { if (capturedPointerId === null) leavePlot(); });
  svg.addEventListener('pointerdown', (event) => {
    if (!update(event)) return;
    if (event.pointerType === 'touch' || event.pointerType === 'pen') {
      capturedPointerId = event.pointerId;
      svg.setPointerCapture?.(event.pointerId);
      event.preventDefault();
    }
  });
  const finishPointer = (event) => {
    if (capturedPointerId !== event.pointerId) return;
    if (svg.hasPointerCapture?.(event.pointerId)) svg.releasePointerCapture(event.pointerId);
    capturedPointerId = null;
    leavePlot();
  };
  svg.addEventListener('pointerup', finishPointer);
  svg.addEventListener('pointercancel', finishPointer);
}

export function preparePerformance(samples, bucketMs, valueField = 'poolHashrate') {
  const points = (Array.isArray(samples) ? samples : [])
    .map((sample) => {
      const time = new Date(sample?.created).getTime();
      const rawValue = sample?.[valueField] ?? (valueField === 'poolHashrate' ? sample?.hashrate : undefined);
      const value = typeof rawValue === 'number' ? rawValue : Number(rawValue);
      return { time, value, source: sample };
    })
    .filter((point) => Number.isFinite(point.time) && Number.isFinite(point.value) && point.value >= 0)
    .sort((a, b) => a.time - b.time);
  while (points.length && Date.now() - points[points.length - 1].time < bucketMs) points.pop();
  return points;
}

function lineSegments(points, gapMs) {
  const segments = [];
  for (const point of points) {
    const last = segments[segments.length - 1];
    if (!last || (last.length && point.time - last[last.length - 1].time > gapMs)) segments.push([point]);
    else last.push(point);
  }
  return segments;
}

function clearContainer(container) {
  while (container.firstChild) container.firstChild.remove();
}

function chartEmpty(container, text) {
  clearContainer(container);
  const empty = document.createElement('div');
  empty.className = 'chart-empty';
  empty.textContent = text;
  container.append(empty);
  return { destroy() {} };
}

export function renderHashrateChart(container, samples, options) {
  const points = preparePerformance(samples, options.bucketMs, options.valueField || 'poolHashrate');
  if (points.length < 2) return chartEmpty(container, options.emptyText);
  let destroyed = false;
  let resizeObserver = null;

  const draw = () => {
    if (destroyed) return;
    clearContainer(container);
    const width = Math.max(560, Math.round(container.clientWidth || 560));
    const compact = width < 700;
    const height = compact ? 240 : 320;
    const margin = compact ? { top: 12, right: 12, bottom: 32, left: 52 } : { top: 16, right: 20, bottom: 34, left: 68 };
    const plotWidth = width - margin.left - margin.right;
    const plotHeight = height - margin.top - margin.bottom;
    const minTime = points[0].time;
    const maxTime = points[points.length - 1].time;
    const maxValue = Math.max(...points.map((point) => point.value), 0);
    const yMax = maxValue > 0 ? maxValue : 1;
    const x = (time) => margin.left + ((time - minTime) / Math.max(1, maxTime - minTime)) * plotWidth;
    const y = (value) => margin.top + plotHeight - (value / yMax) * plotHeight;
    const svg = svgElement('svg', { class: 'chart-svg', viewBox: `0 0 ${width} ${height}`, role: 'img', 'aria-labelledby': `${options.id}-title ${options.id}-desc` });
    svg.append(svgElement('title', { id: `${options.id}-title` }, options.title));
    svg.append(svgElement('desc', { id: `${options.id}-desc` }, options.summary));
    const defs = svgElement('defs');
    const gradient = svgElement('linearGradient', { id: `${options.id}-area`, x1: '0', y1: '0', x2: '0', y2: '1' });
    gradient.append(svgElement('stop', { offset: '0', 'stop-color': options.color, 'stop-opacity': '.24' }));
    gradient.append(svgElement('stop', { offset: '1', 'stop-color': options.color, 'stop-opacity': '0' }));
    defs.append(gradient);
    svg.append(defs);

    const grid = svgElement('g', { class: 'chart-grid' });
    for (let index = 0; index <= 4; index += 1) {
      const gy = margin.top + (plotHeight * index) / 4;
      const value = yMax * (1 - index / 4);
      grid.append(svgElement('line', { x1: margin.left, y1: gy, x2: width - margin.right, y2: gy }));
      grid.append(svgElement('text', { x: margin.left - 8, y: gy + 4, 'text-anchor': 'end' }, options.formatValue(value)));
    }
    for (let index = 0; index <= 3; index += 1) {
      const gx = margin.left + (plotWidth * index) / 3;
      const timestamp = minTime + ((maxTime - minTime) * index) / 3;
      grid.append(svgElement('line', { x1: gx, y1: margin.top, x2: gx, y2: margin.top + plotHeight }));
      grid.append(svgElement('text', { x: gx, y: height - 8, 'text-anchor': index === 0 ? 'start' : index === 3 ? 'end' : 'middle' }, options.formatTime(new Date(timestamp))));
    }
    svg.append(grid);

    const segments = lineSegments(points, options.bucketMs * 2.5);
    for (const segment of segments) {
      if (segment.length < 2) continue;
      const linePath = segment.map((point, index) => `${index ? 'L' : 'M'}${x(point.time).toFixed(1)} ${y(point.value).toFixed(1)}`).join(' ');
      const areaPath = `${linePath} L${x(segment[segment.length - 1].time).toFixed(1)} ${(margin.top + plotHeight).toFixed(1)} L${x(segment[0].time).toFixed(1)} ${(margin.top + plotHeight).toFixed(1)} Z`;
      svg.append(svgElement('path', { class: 'chart-area', d: areaPath, fill: `url(#${options.id}-area)` }));
      svg.append(svgElement('path', { class: 'chart-line-underlay', d: linePath }));
      svg.append(svgElement('path', { class: 'chart-line', d: linePath, stroke: options.color }));
    }

    const hitArea = svgElement('rect', {
      class: 'chart-hit-area', x: margin.left, y: margin.top, width: plotWidth, height: plotHeight,
      fill: 'transparent', 'pointer-events': 'all', 'aria-hidden': 'true',
    });
    svg.append(hitArea);
    const crosshair = svgElement('line', { class: 'chart-crosshair', x1: margin.left, y1: margin.top, x2: margin.left, y2: margin.top + plotHeight, hidden: 'hidden' });
    svg.append(crosshair);
    const tooltip = svgElement('g', { class: 'chart-tooltip', hidden: 'hidden' });
    tooltip.append(svgElement('rect', { x: 0, y: 0, width: 210, height: 54, rx: 8 }));
    const tooltipTime = svgElement('text', { x: 12, y: 21 });
    const tooltipValue = svgElement('text', { x: 12, y: 43, class: 'chart-tooltip-value' });
    tooltip.append(tooltipTime, tooltipValue);

    const pointsGroup = svgElement('g', { class: 'chart-points' });
    const pointCircles = [];
    let activeIndex = -1;
    let focusedIndex = -1;
    const showPoint = (index) => {
      const point = points[index];
      const anchorX = x(point.time);
      const anchorY = y(point.value);
      if (activeIndex >= 0 && activeIndex !== index) {
        pointCircles[activeIndex].setAttribute('r', 4);
        pointCircles[activeIndex].removeAttribute('stroke');
        pointCircles[activeIndex].removeAttribute('stroke-width');
      }
      activeIndex = index;
      pointCircles[index].setAttribute('r', 6);
      pointCircles[index].setAttribute('stroke', '#DEFBFC');
      pointCircles[index].setAttribute('stroke-width', 2);
      crosshair.setAttribute('x1', anchorX);
      crosshair.setAttribute('x2', anchorX);
      crosshair.removeAttribute('hidden');
      tooltipTime.textContent = options.formatDateTime(new Date(point.time));
      tooltipValue.textContent = options.formatValue(point.value);
      const position = anchoredTooltipPosition(anchorX, anchorY, 210, 54, width, height);
      tooltip.setAttribute('transform', `translate(${position.x.toFixed(1)} ${position.y.toFixed(1)})`);
      tooltip.removeAttribute('hidden');
    };
    const hidePoint = () => {
      if (activeIndex >= 0) {
        pointCircles[activeIndex].setAttribute('r', 4);
        pointCircles[activeIndex].removeAttribute('stroke');
        pointCircles[activeIndex].removeAttribute('stroke-width');
      }
      activeIndex = -1;
      crosshair.setAttribute('hidden', 'hidden');
      tooltip.setAttribute('hidden', 'hidden');
    };
    const leavePointer = () => { if (focusedIndex >= 0) showPoint(focusedIndex); else hidePoint(); };
    points.forEach((point, index) => {
      const circle = svgElement('circle', {
        class: 'chart-point', cx: x(point.time), cy: y(point.value), r: 4,
        fill: options.color, tabindex: 0, role: 'img',
        'aria-label': `${options.formatDateTime(new Date(point.time))}: ${options.formatValue(point.value)}`,
      });
      circle.addEventListener('focus', () => { focusedIndex = index; showPoint(index); });
      circle.addEventListener('blur', () => { focusedIndex = -1; hidePoint(); });
      pointCircles.push(circle);
      pointsGroup.append(circle);
    });
    svg.append(pointsGroup);
    svg.append(tooltip);
    bindPlotPointerTracking(svg, points.map((point) => x(point.time)), {
      left: margin.left, right: margin.left + plotWidth, top: margin.top, bottom: margin.top + plotHeight,
      viewWidth: width, viewHeight: height,
    }, showPoint, leavePointer);
    container.append(svg);
    const summary = document.createElement('p');
    summary.className = 'sr-only';
    summary.textContent = options.summary;
    container.append(summary);
  };

  draw();
  if ('ResizeObserver' in window) {
    let lastWidth = container.clientWidth;
    resizeObserver = new ResizeObserver((entries) => {
      const nextWidth = entries[0]?.contentRect.width || 0;
      if (Math.abs(nextWidth - lastWidth) > 8) { lastWidth = nextWidth; draw(); }
    });
    resizeObserver.observe(container);
  }
  return { destroy() { destroyed = true; resizeObserver?.disconnect(); clearContainer(container); } };
}

export function renderBlocksTimeline(container, blocks, options) {
  const points = (Array.isArray(blocks) ? blocks : [])
    .map((block) => ({ block, time: new Date(block?.created).getTime() }))
    .filter((point) => Number.isFinite(point.time))
    .sort((a, b) => a.time - b.time);
  if (!points.length) return chartEmpty(container, options.emptyText);
  let destroyed = false;
  let resizeObserver = null;
  const draw = () => {
    if (destroyed) return;
    clearContainer(container);
    const width = Math.max(560, Math.round(container.clientWidth || 560));
    const height = width < 700 ? 200 : 220;
    const left = width < 700 ? 24 : 40;
    const right = left;
    const baseline = Math.round(height * 0.55);
    const minTime = points[0].time;
    const maxTime = points[points.length - 1].time;
    const x = (time) => left + ((time - minTime) / Math.max(1, maxTime - minTime)) * (width - left - right);
    const svg = svgElement('svg', { class: 'chart-svg block-timeline', viewBox: `0 0 ${width} ${height}`, role: 'img', 'aria-labelledby': `${options.id}-title ${options.id}-desc` });
    svg.append(svgElement('title', { id: `${options.id}-title` }, options.title));
    svg.append(svgElement('desc', { id: `${options.id}-desc` }, options.summary));
    svg.append(svgElement('line', { class: 'timeline-axis', x1: left, y1: baseline, x2: width - right, y2: baseline }));
    svg.append(svgElement('rect', {
      class: 'chart-hit-area', x: left, y: 8, width: width - left - right, height: height - 16,
      fill: 'transparent', 'pointer-events': 'all', 'aria-hidden': 'true',
    }));
    const crosshair = svgElement('line', { class: 'chart-crosshair', x1: left, y1: 8, x2: left, y2: height - 8, hidden: 'hidden' });
    svg.append(crosshair);
    const tooltip = svgElement('g', { class: 'chart-tooltip', hidden: 'hidden' });
    tooltip.append(svgElement('rect', { x: 0, y: 0, width: 260, height: 70, rx: 8 }));
    const lineOne = svgElement('text', { x: 12, y: 23 });
    const lineTwo = svgElement('text', { x: 12, y: 45, class: 'chart-tooltip-value' });
    const lineThree = svgElement('text', { x: 12, y: 63 });
    tooltip.append(lineOne, lineTwo, lineThree);
    const renderedPoints = [];
    let activeIndex = -1;
    let focusedIndex = -1;
    points.forEach((point, index) => {
      const status = options.status(point.block.status);
      const color = options.colors[status.kind] || options.colors.unknown;
      const cx = x(point.time);
      const cy = baseline + (index % 2 ? 18 : -18);
      svg.append(svgElement('line', { class: 'timeline-stem', x1: cx, y1: baseline, x2: cx, y2: cy }));
      const group = svgElement('g', { class: 'timeline-point', tabindex: 0, role: 'img', 'aria-label': options.pointLabel(point.block, status.text) });
      if (point.block.solo) group.append(svgElement('circle', { cx, cy, r: 9, fill: 'none', stroke: options.colors.solo, 'stroke-width': 2 }));
      const primaryCircle = svgElement('circle', { cx, cy, r: 5, fill: color });
      group.append(primaryCircle);
      group.addEventListener('focus', () => { focusedIndex = index; showPoint(index); });
      group.addEventListener('blur', () => { focusedIndex = -1; hidePoint(); });
      renderedPoints.push({ point, status, cx, cy, primaryCircle });
      svg.append(group);
    });
    function showPoint(index) {
      const rendered = renderedPoints[index];
      if (activeIndex >= 0 && activeIndex !== index) {
        renderedPoints[activeIndex].primaryCircle.setAttribute('r', 5);
        renderedPoints[activeIndex].primaryCircle.removeAttribute('stroke');
        renderedPoints[activeIndex].primaryCircle.removeAttribute('stroke-width');
      }
      activeIndex = index;
      rendered.primaryCircle.setAttribute('r', 7);
      rendered.primaryCircle.setAttribute('stroke', '#DEFBFC');
      rendered.primaryCircle.setAttribute('stroke-width', 2);
      crosshair.setAttribute('x1', rendered.cx);
      crosshair.setAttribute('x2', rendered.cx);
      crosshair.removeAttribute('hidden');
      lineOne.textContent = options.formatDateTime(new Date(rendered.point.time));
      lineTwo.textContent = `${options.heightLabel} ${options.formatInteger(rendered.point.block.blockHeight)}`;
      lineThree.textContent = `${rendered.status.text} · ${options.rewardLabel} ${options.formatAmount(rendered.point.block.reward, rendered.point.block)}`;
      const position = anchoredTooltipPosition(rendered.cx, rendered.cy, 260, 70, width, height);
      tooltip.setAttribute('transform', `translate(${position.x.toFixed(1)} ${position.y.toFixed(1)})`);
      tooltip.removeAttribute('hidden');
    }
    function hidePoint() {
      if (activeIndex >= 0) {
        renderedPoints[activeIndex].primaryCircle.setAttribute('r', 5);
        renderedPoints[activeIndex].primaryCircle.removeAttribute('stroke');
        renderedPoints[activeIndex].primaryCircle.removeAttribute('stroke-width');
      }
      activeIndex = -1;
      crosshair.setAttribute('hidden', 'hidden');
      tooltip.setAttribute('hidden', 'hidden');
    }
    const leavePointer = () => { if (focusedIndex >= 0) showPoint(focusedIndex); else hidePoint(); };
    svg.append(tooltip);
    bindPlotPointerTracking(svg, renderedPoints.map((point) => point.cx), {
      left, right: width - right, top: 8, bottom: height - 8, viewWidth: width, viewHeight: height,
    }, showPoint, leavePointer);
    container.append(svg);
    const summary = document.createElement('p');
    summary.className = 'sr-only';
    summary.textContent = options.summary;
    container.append(summary);
  };
  draw();
  if ('ResizeObserver' in window) {
    let lastWidth = container.clientWidth;
    resizeObserver = new ResizeObserver((entries) => {
      const nextWidth = entries[0]?.contentRect.width || 0;
      if (Math.abs(nextWidth - lastWidth) > 8) { lastWidth = nextWidth; draw(); }
    });
    resizeObserver.observe(container);
  }
  return { destroy() { destroyed = true; resizeObserver?.disconnect(); clearContainer(container); } };
}
