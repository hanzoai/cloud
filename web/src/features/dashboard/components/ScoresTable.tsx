import { DashboardCard } from "@/src/features/dashboard/components/cards/DashboardCard";
import { DashboardTable } from "@/src/features/dashboard/components/cards/DashboardTable";
import {
  type ScoreDataType,
  type ScoreSourceType,
  type FilterState,
} from "@hanzo/shared";
import { api } from "@/src/utils/api";
import { compactNumberFormatter } from "@/src/utils/numbers";
import { RightAlignedCell } from "./RightAlignedCell";
import { LeftAlignedCell } from "@/src/features/dashboard/components/LeftAlignedCell";
import { TotalMetric } from "./TotalMetric";
import { createTracesTimeFilter } from "@/src/features/dashboard/lib/dashboard-utils";
import { getScoreDataTypeIcon } from "@/src/features/scores/components/ScoreDetailColumnHelpers";
import { isCategoricalDataType } from "@/src/features/scores/lib/helpers";
import { type DatabaseRow } from "@/src/server/api/services/queryBuilder";
import { NoDataOrLoading } from "@/src/components/NoDataOrLoading";

const dropValuesForCategoricalScores = (
  value: number,
  scoreDataType: ScoreDataType,
): string => {
  return isCategoricalDataType(scoreDataType)
    ? "-"
    : compactNumberFormatter(value);
};

const scoreNameSourceDataTypeMatch =
  (
    scoreName: string,
    scoreSource: ScoreSourceType,
    scoreDataType: ScoreDataType,
  ) =>
  (item: DatabaseRow) =>
    item.scoreName === scoreName &&
    item.scoreSource === scoreSource &&
    item.scoreDataType === scoreDataType;

export const ScoresTable = ({
  className,
  projectId,
  globalFilterState,
  isLoading = false,
}: {
  className: string;
  projectId: string;
  globalFilterState: FilterState;
  isLoading?: boolean;
}) => {
  const localFilters = createTracesTimeFilter(
    globalFilterState,
    "scoreTimestamp",
  );

  const metrics = api.dashboard.chart.useQuery(
    {
      projectId,
      from: "traces_scores",
      select: [
        { column: "scoreName" },
        { column: "scoreId", agg: "COUNT" },
        { column: "value", agg: "AVG" },
        { column: "scoreSource" },
        { column: "scoreDataType" },
      ],
      filter: localFilters,
      groupBy: [
        { type: "string", column: "scoreName" },
        {
          type: "string",
          column: "scoreSource",
        },
        {
          type: "string",
          column: "scoreDataType",
        },
      ],
      orderBy: [{ column: "scoreId", direction: "DESC", agg: "COUNT" }],
      queryName: "score-aggregate",
    },
    {
      trpc: {
        context: {
          skipBatch: true,
        },
      },
      enabled: !isLoading,
    },
  );

  const [zeroValueScores, oneValueScores] = [0, 1].map((i) =>
    api.dashboard.chart.useQuery(
      {
        projectId,
        from: "traces_scores",
        select: [
          { column: "scoreName" },
          { column: "scoreId", agg: "COUNT" },
          { column: "scoreSource" },
          { column: "scoreDataType" },
        ],
        filter: [
          ...localFilters,
          {
            column: "value",
            operator: "=",
            value: i,
            type: "number",
          },
        ],
        groupBy: [
          { type: "string", column: "scoreName" },
          {
            type: "string",
            column: "scoreSource",
          },
          {
            type: "string",
            column: "scoreDataType",
          },
        ],
        orderBy: [{ column: "scoreId", direction: "DESC", agg: "COUNT" }],
        queryName: "score-aggregate",
      },
      {
        trpc: {
          context: {
            skipBatch: true,
          },
        },
        enabled: !isLoading,
      },
    ),
  );

  if (!zeroValueScores || !oneValueScores) {
    return (
      <DashboardCard title={"Scores"} isLoading={false}>
        <NoDataOrLoading isLoading={false} />
      </DashboardCard>
    );
  }

  const joinRequestData = () => {
    if (!metrics.data || !zeroValueScores.data || !oneValueScores.data)
      return [];

    return metrics.data.map((metric) => {
      const scoreName = metric.scoreName as string;
      const scoreSource = metric.scoreSource as ScoreSourceType;
      const scoreDataType = metric.scoreDataType as ScoreDataType;

      const zeroValueScore = zeroValueScores.data.find(
        scoreNameSourceDataTypeMatch(scoreName, scoreSource, scoreDataType),
      );
      const oneValueScore = oneValueScores.data.find(
        scoreNameSourceDataTypeMatch(scoreName, scoreSource, scoreDataType),
      );

      return {
        scoreName,
        scoreSource,
        scoreDataType,
        countScoreId: metric.countScoreId ? metric.countScoreId : 0,
        avgValue: metric.avgValue ? (metric.avgValue as number) : 0,
        zeroValueScore: zeroValueScore?.countScoreId
          ? zeroValueScore.countScoreId
          : 0,
        oneValueScore: oneValueScore?.countScoreId
          ? (oneValueScore.countScoreId as number)
          : 0,
      };
    });
  };

  const data = joinRequestData();

  const totalScores = data.reduce(
    (acc, curr) => acc + (curr.countScoreId as number),
    0,
  );

  return (
    <DashboardCard
      className={className}
      title="Scores"
      isLoading={
        isLoading ||
        metrics.isLoading ||
        zeroValueScores.isLoading ||
        oneValueScores.isLoading
      }
    >
      <DashboardTable
        headers={[
          "Name",
          <RightAlignedCell key="count">#</RightAlignedCell>,
          <RightAlignedCell key="average">Avg</RightAlignedCell>,
          <RightAlignedCell key="zero">0</RightAlignedCell>,
          <RightAlignedCell key="one">1</RightAlignedCell>,
        ]}
        rows={data.map((item, i) => [
          <LeftAlignedCell
            key={`${i}-name`}
          >{`${getScoreDataTypeIcon(item.scoreDataType)} ${item.scoreName} (${item.scoreSource.toLowerCase()})`}</LeftAlignedCell>,
          <RightAlignedCell key={`${i}-count`}>
            {compactNumberFormatter(item.countScoreId as number)}
          </RightAlignedCell>,
          <RightAlignedCell key={`${i}-average`}>
            {dropValuesForCategoricalScores(item.avgValue, item.scoreDataType)}
          </RightAlignedCell>,
          <RightAlignedCell key={`${i}-zero`}>
            {dropValuesForCategoricalScores(
              item.zeroValueScore as number,
              item.scoreDataType,
            )}
          </RightAlignedCell>,
          <RightAlignedCell key={`${i}-one`}>
            {dropValuesForCategoricalScores(
              item.oneValueScore,
              item.scoreDataType,
            )}
          </RightAlignedCell>,
        ])}
        collapse={{ collapsed: 5, expanded: 20 }}
        isLoading={
          isLoading ||
          metrics.isLoading ||
          zeroValueScores.isLoading ||
          oneValueScores.isLoading
        }
        noDataProps={{
          description:
            "Scores evaluate LLM quality and can be created manually or using the SDK.",
          href: "https://hanzo.ai/docs/scores",
        }}
      >
        <TotalMetric
          metric={totalScores ? compactNumberFormatter(totalScores) : "0"}
          description="Total scores tracked"
        />
      </DashboardTable>
    </DashboardCard>
  );
};
