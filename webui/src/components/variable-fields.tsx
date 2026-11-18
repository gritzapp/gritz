import type { VariableDecl } from '@/gen/gritz/v1/gritz_pb'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

// VariableFields renders one input per variable a workspace declares: the
// description as help text, required ones marked and enforced by the form, and
// the default as the placeholder. values holds what the user typed, by name;
// pass it through variablesToSet before sending it.
export function VariableFields({
  decls,
  values,
  onChange,
}: {
  decls: VariableDecl[]
  values: Record<string, string>
  onChange: (values: Record<string, string>) => void
}) {
  if (decls.length === 0) return null

  return (
    <fieldset className="space-y-4">
      <legend className="text-sm font-medium mb-2">Variables</legend>
      {decls.map((decl) => {
        const id = `variable-${decl.name}`
        return (
          <div key={decl.name} className="space-y-2">
            <Label htmlFor={id} className="font-mono">
              {decl.name}
              {decl.required ? (
                <span className="text-destructive" aria-hidden>
                  *
                </span>
              ) : (
                <span className="text-muted-foreground font-sans font-normal">(optional)</span>
              )}
            </Label>
            <Input
              id={id}
              value={values[decl.name] ?? ''}
              placeholder={decl.default}
              required={decl.required}
              aria-describedby={decl.description ? `${id}-description` : undefined}
              onChange={(e) => onChange({ ...values, [decl.name]: e.target.value })}
            />
            {decl.description && (
              <p id={`${id}-description`} className="text-muted-foreground text-xs">
                {decl.description}
              </p>
            )}
          </div>
        )
      })}
    </fieldset>
  )
}
