import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ChatPersonSchema } from "../../../gen/podium/agent/v1/agent_pb";
import { ChatPeople } from "./ChatPeople";
import { Avatar, OriginMark } from "./Origin";

describe("Avatar", () => {
  it("shows a Podium person's Google picture, then their account's, then initials", () => {
    const { container } = render(<Avatar name="Ada Lovelace" login="ada@acme.com" src="https://avatars.example/ada" />);
    const img = () => container.querySelector("img");
    expect(img()).toHaveAttribute("src", "/auth/picture?login=ada%40acme.com");

    fireEvent.error(img()!);
    expect(img()).toHaveAttribute("src", "https://avatars.example/ada");

    fireEvent.error(img()!);
    expect(img()).toBeNull();
    expect(container).toHaveTextContent("AL");
  });

  it("asks for no Google picture for somebody who is not in Podium", () => {
    const { container } = render(<Avatar name="Dana" src="https://avatars.example/dana" />);
    expect(container.querySelector("img")).toHaveAttribute("src", "https://avatars.example/dana");
  });
});

describe("ChatPeople", () => {
  it("draws each face as a flex item, on the same line as the chips", () => {
    const { getAllByTestId } = render(
      <ChatPeople people={[create(ChatPersonSchema, { login: "ada@acme.com", name: "Ada", via: "github" })]} />,
    );
    expect(getAllByTestId("chat-person")[0]).toHaveClass("flex");
  });
});

describe("OriginMark", () => {
  it("draws Linear's mark on a message said in Linear, linking back to it", () => {
    const { getByTestId } = render(<OriginMark via="linear" url="https://linear.app/acme/issue/EME-5" />);
    const mark = getByTestId("chat-origin");
    expect(mark).toHaveAttribute("href", "https://linear.app/acme/issue/EME-5");
    expect(mark).toHaveAttribute("aria-label", "Open in Linear");
    expect(mark.querySelector("img")).not.toBeNull();
  });

  it("says a Linear participant joined from Linear", () => {
    const { getByTestId } = render(
      <ChatPeople people={[create(ChatPersonSchema, { login: "ada@acme.com", name: "Ada", via: "linear" })]} />,
    );
    expect(getByTestId("chat-person")).toHaveAttribute("aria-label", expect.stringContaining("Linear"));
  });
});
