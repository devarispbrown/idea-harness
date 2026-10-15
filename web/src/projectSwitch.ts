// Which project the shell shows (UI plan Phase 3: the project switcher).
//
// The daemon serves every project registered with it; the UI used to show
// the first one, always. Three sources decide now, in order: the project the
// address names (`#/p/<id>/…` -- a deep link, a push notification), the one
// this browser last chose, and the first registered.

export interface ProjectRef {
  project_id: string;
  name: string;
}

export const LAST_PROJECT_KEY = "idea-harness:last-project";

export interface ProjectChoice {
  projectId: string;
  // unknown is set when the address named a project the daemon does not
  // serve: the shell says so rather than quietly showing another project
  // under a link that promised this one.
  unknown?: string;
}

export function pickProject(projects: ProjectRef[], fromHash: string | null, remembered: string | null): ProjectChoice {
  const has = (id: string | null) => !!id && projects.some((p) => p.project_id === id);
  if (fromHash) {
    if (has(fromHash)) return { projectId: fromHash };
    return { projectId: projects[0]?.project_id ?? "", unknown: fromHash };
  }
  if (has(remembered)) return { projectId: remembered! };
  return { projectId: projects[0]?.project_id ?? "" };
}

// rememberProject / rememberedProject persist the last choice. Storage can be
// unavailable (a locked-down profile, Safari's private mode on old versions);
// the switcher then simply forgets, it never throws.
export function rememberProject(projectId: string, storage: Pick<Storage, "setItem"> | undefined = safeStorage()): void {
  try {
    storage?.setItem(LAST_PROJECT_KEY, projectId);
  } catch {
    /* not persisted */
  }
}

export function rememberedProject(storage: Pick<Storage, "getItem"> | undefined = safeStorage()): string | null {
  try {
    return storage?.getItem(LAST_PROJECT_KEY) ?? null;
  } catch {
    return null;
  }
}

function safeStorage(): Storage | undefined {
  try {
    return typeof window !== "undefined" ? window.localStorage : undefined;
  } catch {
    return undefined;
  }
}

// projectLabel disambiguates two projects with the same name (two checkouts
// of one repository) by the end of their id.
export function projectLabel(p: ProjectRef, all: ProjectRef[]): string {
  const name = p.name || p.project_id.slice(0, 8);
  const twins = all.filter((x) => x.name === p.name).length > 1;
  return twins ? `${name} (${p.project_id.slice(0, 6)})` : name;
}
