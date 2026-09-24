import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { Skeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import { Card, CardContent, CardFooter, CardHeader, CardTitle } from "../ui/card";
import { Textarea } from "../ui/textarea";

/**
 * ProfileFileCard edits profile.yaml on the conductor's host as text. A file that does not
 * load is refused and the previous one stays, so a bad save cannot break a bot that is
 * answering.
 */
export function ProfileFileCard() {
  const file = useQuery({
    queryKey: ["agent", "profile-file"],
    queryFn: () => agent.getProfileFile({}),
  });

  if (isAgentUnreachable(file.error)) return null;
  if (file.isError) {
    return (
      <Alert variant="destructive" role="alert" className="max-w-4xl">
        {errorMessage(file.error)}
      </Alert>
    );
  }
  if (!file.data) {
    return (
      <Card className="max-w-4xl" aria-busy="true">
        <CardContent className="pt-5">
          <Skeleton className="h-64 w-full" />
        </CardContent>
      </Card>
    );
  }
  return <ProfileFileEditor key={file.data.content} content={file.data.content} path={file.data.path} />;
}

function ProfileFileEditor({ content, path }: { content: string; path: string }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [text, setText] = useState(content);
  const [error, setError] = useState<string>();

  const save = useMutation({
    mutationFn: () => agent.updateProfileFile({ content: text }),
    onSuccess: async () => {
      setError(undefined);
      toast("profile.yaml saved. It applies to the next turn.", "ok");
      await qc.invalidateQueries({ queryKey: ["agent", "profile"] });
      await qc.invalidateQueries({ queryKey: ["agent", "profile-file"] });
    },
    onError: (err) => setError(errorMessage(err)),
  });

  return (
    <form
      data-testid="profile-file-card"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <Card className="max-w-4xl">
        <CardHeader>
          <div>
            <CardTitle>profile.yaml</CardTitle>
            <p className="text-xs text-muted">
              <code className="font-mono text-fg">{path}</code>
            </p>
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          <Textarea
            aria-label="profile.yaml"
            value={text}
            onChange={(e) => setText(e.target.value)}
            spellCheck={false}
            rows={24}
            className="font-mono text-xs"
          />
          {error ? (
            <Alert variant="destructive" role="alert">
              {error}
            </Alert>
          ) : null}
        </CardContent>
        <CardFooter>
          <Button
            type="submit"
            size="sm"
            data-testid="profile-file-save"
            disabled={save.isPending || text === content}
          >
            {save.isPending ? "Saving…" : "Save profile.yaml"}
          </Button>
          <span className="text-xs text-muted">
            A file that does not load is refused and the previous one stays.
          </span>
        </CardFooter>
      </Card>
    </form>
  );
}
