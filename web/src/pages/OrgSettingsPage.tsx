import { useState, type ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Building2, Plug, Repeat2, Users } from "lucide-react";
import { Navigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AssistantPanel,
  Issue,
  PanelBody,
  Row,
  SaveBar,
  ScopeBar,
  SectionTabs,
} from "../components/agent/AssistantPanel";
import { ConnectionsPanel } from "../components/agent/ConnectionsPanel";
import { RoutinesAdminPanel } from "../components/agent/RoutinesAdminPanel";
import { PageFrame } from "../components/PageHeader";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { HOUSE_NAME } from "../lib/brand";
import { errorMessage, organization } from "../lib/client";
import { useViewer, type Viewer } from "../lib/identity";
import { canManageInfra } from "../lib/rbac";
import { relative } from "../lib/format";
import { UsersPage } from "./UsersPage";

/** The caps podium-server enforces (store/organization.go). */
const MAX_ORGANIZATION_NAME = 80;
const MAX_DEPLOYMENT_NAME = 40;

type Section = {
  id: string;
  title: string;
  icon: LucideIcon;
  lead: string;
  show: (v: Viewer | undefined) => boolean;
  /**
   * The section draws its own table edge to edge, so the panel adds no padding. General
   * draws its own rows and save bar, the way an assistant's sections do.
   */
  flush?: boolean;
  render: () => ReactNode;
};

const conductorAdmin = (v: Viewer | undefined) => Boolean(v?.agentEnabled) && canManageInfra(v);

/**
 * SECTIONS is everything that is set once for the whole organization rather than per person.
 * Each used to be its own sidebar entry, spread across Access, Compute and Organization; one
 * of them, Routines, appeared twice. A member sees General and Members, read-only.
 */
const SECTIONS: Section[] = [
  {
    id: "general",
    icon: Building2,
    title: "General",
    lead: "What this deployment is called, and the organization it serves. Both show across the app and in what the assistant says.",
    show: () => true,
    flush: true,
    render: () => <GeneralSection />,
  },
  {
    id: "members",
    icon: Users,
    title: "Members",
    lead: "The people who can sign in, and what each of them is allowed to do.",
    show: () => true,
    flush: true,
    render: () => <UsersPage />,
  },
  {
    id: "connections",
    icon: Plug,
    title: "Connections",
    lead: "The outside apps your assistants work with. GitHub lets them read code and open pull requests, and Slack lets people message them.",
    show: conductorAdmin,
    flush: true,
    render: () => <ConnectionsPanel />,
  },
  {
    id: "routines",
    icon: Repeat2,
    title: "Routines policy",
    lead: "How many routines each person may have and spend on, and everyone's routines at a glance. Your own are on Routines.",
    show: conductorAdmin,
    flush: true,
    render: () => <RoutinesAdminPanel />,
  },
];

/** The sections this viewer gets, in sidebar order. */
function sectionsFor(v: Viewer | undefined): Section[] {
  return SECTIONS.filter((s) => s.show(v));
}

/**
 * OrgSettingsPage is Organization settings: one page, a section per setting the whole
 * organization shares. /org/settings opens General; a section this viewer does not get
 * lands there too.
 */
export function OrgSettingsPage() {
  const viewer = useViewer();
  const id = useParams()["*"]?.split("/").filter(Boolean)[1] ?? "";
  const sections = sectionsFor(viewer);
  const section = sections.find((s) => s.id === id);
  if (!section) return <Navigate to={`/org/settings/${sections[0]?.id ?? "general"}`} replace />;

  // Drawn in the same parts as an assistant (components/agent/AssistantPanel): one panel,
  // a tab strip of links so each section keeps its address, the raised bar saying what the
  // open section is for, then the section itself.
  return (
    <PageFrame title="Settings" bodyClassName="p-6">
      <AssistantPanel label={section.title}>
        <SectionTabs
          label="Organization settings"
          tabs={sections.map((s) => ({ id: s.id, label: s.title, icon: s.icon }))}
          current={section.id}
          href={(sid) => `/org/settings/${sid}`}
        />
        <ScopeBar>{section.lead}</ScopeBar>
        <div className={section.flush ? undefined : "px-6 py-6"}>{section.render()}</div>
      </AssistantPanel>
    </PageFrame>
  );
}

/**
 * GeneralSection names the deployment and its organization. The deployment name replaces
 * "Podium" in the sidebar, the browser tab, the sign-in page and what the assistant calls
 * itself; the organization name tells the assistant whom it works for.
 */
function GeneralSection() {
  const viewer = useViewer();
  const manage = canManageInfra(viewer);
  const qc = useQueryClient();
  const toast = useToast();
  const query = useQuery({
    queryKey: ["organization"],
    queryFn: () => organization.getOrganization({}),
  });
  const saved = query.data?.organization;
  const [draft, setDraft] = useState<{ org: string; deployment: string }>();
  const form = draft ?? { org: saved?.organizationName ?? "", deployment: saved?.deploymentName ?? "" };
  const dirty =
    draft !== undefined &&
    (draft.org.trim() !== (saved?.organizationName ?? "") ||
      draft.deployment.trim() !== (saved?.deploymentName ?? ""));
  const tooLong =
    form.org.trim().length > MAX_ORGANIZATION_NAME || form.deployment.trim().length > MAX_DEPLOYMENT_NAME;

  const save = useMutation({
    mutationFn: () =>
      organization.updateOrganization({
        organizationName: form.org.trim(),
        deploymentName: form.deployment.trim(),
      }),
    onSuccess: async () => {
      setDraft(undefined);
      // WhoAmI carries the names to the header, the tab title and every useBrand.
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["organization"] }),
        qc.invalidateQueries({ queryKey: ["whoami"] }),
      ]);
      toast("Saved. The assistant uses the new names from its next turn.", "ok");
    },
    onError: (err) => toast(`UpdateOrganization: ${errorMessage(err)}`),
  });

  if (query.isError) {
    return (
      <div className="px-6 py-6">
        <Alert variant="destructive" title="Could not read the organization">
          {errorMessage(query.error)}
        </Alert>
      </div>
    );
  }

  const shown = form.deployment.trim() || HOUSE_NAME;
  const org = form.org.trim();
  const status = !manage
    ? "Only an admin or owner can change these."
    : dirty
      ? "Unsaved changes."
      : saved?.updatedAt && saved.updatedBy
        ? `Last changed ${relative(saved.updatedAt)} by ${saved.updatedBy}.`
        : `Not set yet, so people see ${HOUSE_NAME}.`;

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (manage && dirty && !tooLong) save.mutate();
      }}
    >
      <PanelBody>
        <Row id="org-name" label="Organization name" hint="The assistant is told it works for this organization.">
          <Input
            id="org-name"
            placeholder="Acme Corp"
            value={form.org}
            maxLength={MAX_ORGANIZATION_NAME}
            disabled={!manage || query.isPending}
            onChange={(e) => setDraft({ ...form, org: e.target.value })}
            className="max-w-md"
            autoComplete="off"
          />
        </Row>
        <Row
          id="deployment-name"
          label="Deployment name"
          hint={`Replaces “${HOUSE_NAME}” in the sidebar, the browser tab and the sign-in page. Leave it empty to keep ${HOUSE_NAME}.`}
        >
          <Input
            id="deployment-name"
            placeholder={HOUSE_NAME}
            value={form.deployment}
            maxLength={MAX_DEPLOYMENT_NAME}
            disabled={!manage || query.isPending}
            onChange={(e) => setDraft({ ...form, deployment: e.target.value })}
            className="max-w-md"
            autoComplete="off"
          />
          {tooLong ? <Issue>Keep the deployment name to {MAX_DEPLOYMENT_NAME} characters.</Issue> : null}
        </Row>
        <Row label="Preview" hint="How the names read once saved.">
          <p className="m-0 max-w-[65ch] text-soft" data-testid="org-preview">
            People will see <strong className="text-fg">{shown}</strong>. The assistant calls itself {shown}
            {org ? (
              <>
                {" "}
                and knows it works for <strong className="text-fg">{org}</strong>
              </>
            ) : null}
            . An assistant you renamed on Assistants keeps its own name.
          </p>
        </Row>
      </PanelBody>
      <SaveBar dirty={dirty} status={status}>
        {manage ? (
          <>
            {dirty ? (
              <Button type="button" variant="ghost" onClick={() => setDraft(undefined)} disabled={save.isPending}>
                Discard
              </Button>
            ) : null}
            <Button type="submit" disabled={!dirty || tooLong || save.isPending}>
              {save.isPending ? "Saving…" : "Save"}
            </Button>
          </>
        ) : null}
      </SaveBar>
    </form>
  );
}
