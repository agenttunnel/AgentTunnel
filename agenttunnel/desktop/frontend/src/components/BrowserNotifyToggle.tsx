import { useBrowserNotify } from "../lib/attention";
import { switchProps } from "../lib/switchProps";

/** Settings section for browser notifications in the web UI. The desktop shell
 * notifies natively, so this renders nothing there. */
export function BrowserNotifyToggle() {
  const { supported, on, denied, toggle } = useBrowserNotify();
  if (!supported) return null;
  return (
    <section className="set-section">
      <h4>Notifications</h4>
      <p className="hint">
        Notify this browser when a turn finishes or an agent asks a question while the tab is in
        the background.
      </p>
      {denied ? (
        <p className="hint warn">
          This browser blocks notifications for this page. Allow them in the site settings, then
          turn this on.
        </p>
      ) : (
        <div className={`toggle ${on ? "on" : ""}`} {...switchProps(on, toggle)}>
          <span className="sw" /> Browser notifications
        </div>
      )}
    </section>
  );
}
