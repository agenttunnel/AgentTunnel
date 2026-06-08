import React, { createContext, useContext } from 'react';
import type { Pipeline } from '../core/Pipeline.js';

interface PipelineContextValue {
  pipeline: Pipeline | null;
}

const PipelineContext = createContext<PipelineContextValue>({ pipeline: null });

export const PipelineProvider = PipelineContext.Provider;

export function usePipeline(): Pipeline | null {
  return useContext(PipelineContext).pipeline;
}
