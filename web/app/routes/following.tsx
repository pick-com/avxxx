import {EmptyState} from '../components/States';

export const meta = () => [{title: 'REEL · 关注'}];

// MobileFollowing tab. There is no follow model yet, so this tab explains that
// instead of showing a feed.
// TODO(tech plan 3.13 follows + GET /api/me/following/feed): followed performers' new and upcoming works.
export default function Following() {
  return (
    <div className="page">
      <EmptyState
        icon="following"
        title="关注演员功能即将上线"
        body="上线后，你关注的演员有新作时会出现在这里。现在可以先去演员库看看。"
        actions={[{to: '/discover?tab=actors', label: '浏览演员库'}]}
      />
    </div>
  );
}
