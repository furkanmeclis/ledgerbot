import * as React from 'react';
import { cva, type VariantProps } from 'class-variance-authority';
import { cn } from '@/lib/utils';

const badgeVariants = cva('inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-[11px] font-medium transition-colors', {
  variants: {
    variant: {
      default: 'border-transparent bg-primary text-primary-foreground',
      secondary: 'border-transparent bg-secondary text-secondary-foreground',
      success: 'border-transparent bg-accent text-accent-foreground',
      warning: 'border-transparent bg-warning/20 text-[oklch(0.45_0.12_70)]',
      destructive: 'border-transparent bg-destructive/15 text-destructive',
      outline: 'text-foreground',
    },
  },
  defaultVariants: { variant: 'default' },
});

export function Badge({ className, variant, ...props }: React.HTMLAttributes<HTMLSpanElement> & VariantProps<typeof badgeVariants>) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />;
}
