import defaultMdxComponents from 'fumadocs-ui/mdx';
import { Accordion, Accordions } from 'fumadocs-ui/components/accordion';
import { File, Files, Folder } from 'fumadocs-ui/components/files';
import { Step, Steps } from 'fumadocs-ui/components/steps';
import { Tab, Tabs } from 'fumadocs-ui/components/tabs';
import { TypeTable } from 'fumadocs-ui/components/type-table';
import type { MDXComponents } from 'mdx/types';
import { BaseUrl, EnvExports, EnvironmentTable } from './env/env-components';

export function getMDXComponents(components?: MDXComponents) {
  return {
    ...defaultMdxComponents,
    Accordion,
    Accordions,
    File,
    Files,
    Folder,
    Step,
    Steps,
    Tab,
    Tabs,
    TypeTable,
    BaseUrl,
    EnvExports,
    EnvironmentTable,
    ...components,
  } satisfies MDXComponents;
}

export const useMDXComponents = getMDXComponents;

declare global {
  type MDXProvidedComponents = ReturnType<typeof getMDXComponents>;
}
