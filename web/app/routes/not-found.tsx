import {EmptyState} from '../components/States';

export const meta = () => [{title: 'REEL · 页面不存在'}];

export default function NotFound() {
  return (
    <div className="page">
      <EmptyState title="页面不存在" body="链接可能有误，或页面已经移走。" actions={[{to: '/', label: '回到首页'}, {to: '/search', label: '搜索', primary: false}]} />
    </div>
  );
}
