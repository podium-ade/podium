/**
 * sectionsOverflow is true when the tab row is wider than the space it has.
 * A zero width means layout has not happened, so the tabs stay.
 */
export const sectionsOverflow = (scrollWidth: number, clientWidth: number): boolean =>
  clientWidth > 0 && scrollWidth > clientWidth + 1;
