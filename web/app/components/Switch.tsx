import type {ReactNode} from 'react';

/** Toggle row: label + optional description + role="switch" button (44px target). */
export function SwitchRow({label, description, checked, onChange, disabled, error, busy}: {
  label: ReactNode;
  description?: ReactNode;
  checked: boolean;
  onChange: (next: boolean) => void;
  disabled?: boolean;
  error?: string;
  busy?: boolean;
}) {
  return (
    <div className="set-row">
      <div className="set-text">
        <span className="set-label">{label}</span>
        {description && <span className="set-desc">{description}</span>}
        {error && (
          <span className="field-error" role="alert">
            {error}
          </span>
        )}
      </div>
      <Switch checked={checked} onChange={onChange} disabled={disabled} label={typeof label === 'string' ? label : undefined} busy={busy} />
    </div>
  );
}

export function Switch({checked, onChange, disabled, label, busy}: {
  checked: boolean;
  onChange: (next: boolean) => void;
  disabled?: boolean;
  label?: string;
  busy?: boolean;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      aria-busy={busy || undefined}
      disabled={disabled}
      className="switch"
      onClick={() => onChange(!checked)}
    >
      <span className="switch-track" aria-hidden="true">
        <span className="switch-knob" />
      </span>
    </button>
  );
}
