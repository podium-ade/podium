import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ChatPersonSchema } from "../../../gen/podium/agent/v1/agent_pb";
import { ChatPeople } from "./ChatPeople";
import { Avatar } from "./Origin";

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
