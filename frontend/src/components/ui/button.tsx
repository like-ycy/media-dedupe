import { cva, type VariantProps } from "class-variance-authority"
import * as React from "react"
import { cn } from "@/lib/utils"

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-1.5 whitespace-nowrap rounded-control font-semibold transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/55 focus-visible:ring-offset-1 focus-visible:ring-offset-background disabled:pointer-events-none disabled:opacity-45 [&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default:
          "bg-primary text-on-primary shadow-sm hover:bg-primary-hover active:bg-primary-active",
        secondary:
          "border border-border-strong bg-surface text-foreground hover:bg-surface-2 active:bg-surface-3",
        ghost: "bg-transparent text-foreground hover:bg-surface-2",
        danger: "bg-transparent text-danger hover:bg-danger-soft",
        link: "text-primary underline-offset-4 hover:underline",
      },
      size: {
        default: "control-h px-3.5 text-sm",
        sm: "control-h-sm px-2.5 text-xs",
        lg: "control-h-lg px-5 text-sm",
        icon: "control-h w-8 px-0",
        "icon-sm": "control-h-sm w-7 px-0",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
)

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {}

function Button({ className, variant, size, type = "button", ...props }: ButtonProps) {
  return (
    <button
      type={type}
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    />
  )
}

export { Button, buttonVariants }
