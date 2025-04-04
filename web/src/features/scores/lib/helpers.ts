import { type ScoreData } from "./types";
import { ScoreDataType } from "@hanzo/shared";

export const isNumericDataType = (dataType: ScoreDataType) =>
  dataType === ScoreDataType.NUMERIC;

export const isCategoricalDataType = (dataType: ScoreDataType) =>
  dataType === ScoreDataType.CATEGORICAL;

export const isBooleanDataType = (dataType: ScoreDataType) =>
  dataType === ScoreDataType.BOOLEAN;

export const isScoreUnsaved = (scoreId?: string): boolean => !scoreId;

export const toOrderedScoresList = (list: ScoreData[]): ScoreData[] =>
  list.sort((a, b) => a.key.localeCompare(b.key));
