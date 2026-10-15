import { projectLabel, type ProjectRef } from "./projectSwitch";

// ProjectSwitcher picks which of the daemon's projects the shell shows. A
// native <select>: it is the control a phone already knows how to present,
// and it is labelled and keyboard-operable with nothing extra. With one
// project there is nothing to switch, so it shows the name only.
export default function ProjectSwitcher({
  projects,
  projectId,
  onSwitch,
}: {
  projects: ProjectRef[];
  projectId: string;
  onSwitch: (projectId: string) => void;
}) {
  if (projects.length < 2) return null;
  return (
    <div className="project-switcher">
      <label className="project-switcher-label" htmlFor="project-switcher">
        Project
      </label>
      <select
        id="project-switcher"
        className="project-switcher-select"
        value={projectId}
        onChange={(e) => onSwitch(e.target.value)}
      >
        {projects.map((p) => (
          <option key={p.project_id} value={p.project_id}>
            {projectLabel(p, projects)}
          </option>
        ))}
      </select>
    </div>
  );
}
