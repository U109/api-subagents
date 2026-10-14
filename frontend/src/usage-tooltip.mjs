/** 将提示放在悬停点或键盘选中柱右侧；空间不足时翻到左侧或上方，并限制在实际图表内。 */
export function placeUsageTooltip(anchor, bounds, size) {
  const padding = 12, gap = 14;
  const maxLeft = Math.max(padding, bounds.width - size.width - padding);
  const maxTop = Math.max(padding, bounds.height - size.height - padding);
  const preferredLeft = anchor.x + gap;
  const preferredTop = anchor.y + gap;
  return {
    left: Math.max(padding, Math.min(maxLeft, preferredLeft <= maxLeft ? preferredLeft : anchor.x - size.width - gap)),
    top: Math.max(padding, Math.min(maxTop, preferredTop <= maxTop ? preferredTop : anchor.y - size.height - gap))
  };
}
