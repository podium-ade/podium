/**
 * BackendMark is a small glyph per agent backend, so a row is recognisable before it is
 * read. Decorative: everywhere it appears, the backend is also named in text.
 */
export function BackendMark({ id }: { id: string }) {
  if (id === "grok") {
    return (
      <svg
        aria-hidden="true"
        viewBox="0 0 16 16"
        className="size-4 shrink-0 text-fg"
        fill="currentColor"
      >
        <path d="M2.4 13.6 8 8 2.4 2.4h2.7L10.7 8l-5.6 5.6H2.4Zm6.9 0L13.6 9.3v4.3H9.3Z" />
      </svg>
    );
  }
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 16 16"
      className="size-4 shrink-0 text-accent"
      fill="currentColor"
    >
      <path d="M8 1.2 9.6 6.4 14.8 8 9.6 9.6 8 14.8 6.4 9.6 1.2 8 6.4 6.4Z" />
    </svg>
  );
}
