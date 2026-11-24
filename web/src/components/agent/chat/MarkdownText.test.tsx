import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import type { ThreadMessageLike } from "@assistant-ui/react";
import { ChatThread } from "./ChatThread";

function mount(text: string) {
  const messages: ThreadMessageLike[] = [{ id: "u1", role: "user", content: [{ type: "text", text }] }];
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>
        <ChatThread botName="Podium" busy={false} messages={messages} onSend={() => {}} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("MarkdownText person mentions", () => {
  it("draws @[Name](mention:login) as one chip with just the name", () => {
    mount("@[Alice Ng](mention:alice@example.com) can you look at this?");
    const chip = screen.getByTestId("chat-mention");
    expect(chip).toHaveTextContent(/^@Alice Ng$/);
    expect(screen.getByTestId("chat-message")).toHaveTextContent("@Alice Ng can you look at this?");
    expect(screen.getByTestId("chat-message")).not.toHaveTextContent("mention:");
    expect(screen.getByTestId("chat-message").querySelector("a")).toBeNull();
  });

  it("handles several, in a list, and next to bold text", () => {
    mount("- **ping** @[A](mention:a@x.io), @[B C](mention:b@x.io)\n- @[D](mention:d@x.io)");
    expect(screen.getAllByTestId("chat-mention").map((e) => e.textContent)).toEqual(["@A", "@B C", "@D"]);
  });

  it("leaves a mention inside code as typed, and an ordinary link alone", () => {
    mount("`@[A](mention:a@x.io)` and [docs](https://example.com)");
    expect(screen.queryByTestId("chat-mention")).toBeNull();
    expect(screen.getByTestId("chat-message")).toHaveTextContent("@[A](mention:a@x.io)");
    expect(screen.getByRole("link", { name: "docs" })).toHaveAttribute("href", "https://example.com");
  });

  it("does not let a name carry markup into the page", () => {
    mount("@[<img src=x onerror=alert(1)>](mention:a@x.io)");
    expect(document.querySelector("img")).toBeNull();
  });
});
